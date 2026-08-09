"use client";

import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { LocalAPIError, LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { ConsoleController, MutationRequest, MutationReview } from "@/features/console/types";
import { normalizeRoute, parseRoute, routeHref, routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { deploymentPollInterval, terminalDeployment } from "@/features/delivery/polling-model";
import { deriveProjectSummary, emptyFoundation, normalizeStatus, PROJECT_SUMMARY_TTL_MS, type FoundationState, type ProjectSummaryEntry } from "@/lib/presentation/project";
import type { BootstrapSession, ConsoleState, ServiceRecord } from "@/lib/contracts/registry";
import { terminalBootstrap } from "@/lib/presentation/infrastructure/model";
import { clearProjectPatch, createRequestLimiter, loadFoundation, loadProject, loadProjectSummary, reconnect, secretBody, workspacePatch, type RequestRunner } from "@/hooks/console-state-support";

export function useConsoleState() {
  const [session, setSession] = useState<LocalSessionStatus | null>(null);
  const [route, setRoute] = useState<ConsoleRoute>({ projectID: "", view: "projects", tab: "" });
  const [projectID, setSelectedProjectID] = useState("");
  const [review, setReview] = useState<(MutationReview & { submit: (key: string, credential?: string) => Promise<string> }) | null>(null);
  const [projectSummaries, setProjectSummaries] = useState<Record<string, ProjectSummaryEntry>>({});
  const revealTimer = useRef<number | null>(null);
  const generation = useRef(0);
  const selectedProject = useRef("");
  const currentRoute = useRef(route);
  const currentSession = useRef<LocalSessionStatus | null>(null);
  const switchQueue = useRef<Promise<void>>(Promise.resolve());
  const summaryCache = useRef(new Map<string, ProjectSummaryEntry>());
  const [state, setState] = useState<ConsoleState & FoundationState>({
    status: "loading",
    message: "",
    projects: [],
    project: null,
    readiness: null,
    nodes: [],
    services: [],
    deployments: [],
    sessions: [],
    bootstrapCommand: "",
    bootstrapCommandSessionID: "",
    bootstrapEvents: [],
    bootstrapEventsSessionID: "",
    deploymentEvents: [],
    audit: [],
    support: null,
    secretReveal: null,
    totpSetup: null,
    incidents: [],
    nodeDetail: null,
    serviceDetail: null,
    busy: "",
    foundation: emptyFoundation,
  });

  const client = useMemo(() => new LocalClient(), []);
  const summaryRequest = useMemo(() => createRequestLimiter(), []);
  const currentProject = state.projects.find((item) => item.id === projectID) ?? null;

  function patch(value: Partial<ConsoleState & FoundationState>) {
    setState((prev) => ({ ...prev, ...value }));
  }

  function clearSensitive() {
    if (revealTimer.current !== null) window.clearTimeout(revealTimer.current);
    revealTimer.current = null;
    patch({ secretReveal: null, totpSetup: null });
  }

  function reviewMutation(request: MutationRequest, submit: (key: string, credential?: string) => Promise<string>) {
    setReview({ ...request, idempotencyKey: crypto.randomUUID(), status: "reviewing", submit });
  }

  async function submitReview(credential?: string) {
    if (!review || review.status === "submitting") return;
    setReview((current) => (current ? { ...current, status: "submitting", error: "", nextAction: "" } : current));
    try {
      const evidence = await review.submit(review.idempotencyKey, credential);
      setReview((current) => (current ? { ...current, status: "succeeded", evidence } : current));
    } catch (cause) {
      const error = cause as LocalAPIError;
      setReview((current) =>
        current
          ? { ...current, status: "failed", error: error.message, nextAction: error.nextAction || "Retry the same reviewed attempt." }
          : current,
      );
    }
  }

  function closeReview() {
    setReview((current) => (current?.status === "submitting" ? current : null));
  }

  function isCurrent(operation: number, selectedProjectID: string) {
    return generation.current === operation && selectedProject.current === selectedProjectID;
  }

  async function loadProjectSummaries(projects: typeof state.projects, agentStatus: string, operation: number, force = false) {
    if (!isCurrent(operation, "")) return;
    const now = Date.now();
    const projectIDs = new Set(projects.map((project) => project.id));
    for (const id of summaryCache.current.keys()) if (!projectIDs.has(id)) summaryCache.current.delete(id);
    const pending = projects.filter((project) => {
      const entry = summaryCache.current.get(project.id);
      return force || entry?.status !== "ready" || entry.stale || !entry.fetchedAt || now - entry.fetchedAt >= PROJECT_SUMMARY_TTL_MS;
    });
    for (const project of pending) {
      const entry = summaryCache.current.get(project.id);
      if (entry?.summary) summaryCache.current.set(project.id, { ...entry, refreshing: true });
    }
    setProjectSummaries(Object.fromEntries(projects.map((project) => [
      project.id,
      summaryCache.current.get(project.id) ?? { status: "loading" as const },
    ])));
    const run: RequestRunner = (request) => summaryRequest(() => {
      if (!isCurrent(operation, "")) return Promise.reject(new Error("Obsolete project summary operation"));
      return request();
    });
    let cursor = 0;
    await Promise.all(Array.from({ length: Math.min(2, pending.length) }, async () => {
      for (;;) {
        const project = pending[cursor++];
        if (!project) return;
        try {
          const entry = await loadProjectSummary(client, project, agentStatus, run);
          if (!isCurrent(operation, "")) return;
          summaryCache.current.set(project.id, entry);
          setProjectSummaries((current) => ({ ...current, [project.id]: entry }));
        } catch (cause) {
          if (!isCurrent(operation, "")) return;
          const cached = summaryCache.current.get(project.id);
          const entry: ProjectSummaryEntry = cached?.summary
            ? { ...cached, status: "ready", refreshing: false, stale: true, error: (cause as Error).message }
            : { status: "error", error: (cause as Error).message };
          summaryCache.current.set(project.id, entry);
          setProjectSummaries((current) => ({ ...current, [project.id]: entry }));
        }
      }
    }));
  }

  async function load(selectedProjectID = selectedProject.current, operation = generation.current, forceSummaries = false) {
    if (!isCurrent(operation, selectedProjectID)) return;
    patch(state.status === "ready" ? { message: "" } : { status: "loading", message: "" });
    try {
      const sessionStatus = await client.session(selectedProjectID || undefined);
      if (!isCurrent(operation, selectedProjectID)) return;
      setSession(sessionStatus);
      currentSession.current = sessionStatus;
      if (!sessionStatus.authenticated) {
        clearSensitive();
        summaryCache.current.clear();
        setProjectSummaries({});
        const cloudUnavailable = sessionStatus.cloud_connected !== "ok";
        const expired = sessionStatus.token_status === "invalid";
        patch({
          status: cloudUnavailable ? "network" : "permission",
          message: cloudUnavailable
            ? "Cloud is unavailable. Local configuration is preserved; retry when Cloud connectivity returns."
            : expired
              ? "The saved Cloud session expired. Sign in again to continue."
              : "Sign in with GitHub to load your Opsi projects.",
          projects: [],
          project: null,
        });
        return;
      }
      const effectiveOrgID = sessionStatus.org_id ?? "";
      if (!effectiveOrgID) throw new Error("Authenticated session did not include an organization ID");
      const list = await client.projects(effectiveOrgID);
      if (!isCurrent(operation, selectedProjectID)) return;
      const projects = list.projects ?? [];
      if (!selectedProjectID) {
        patch(workspacePatch(projects));
        void loadProjectSummaries(projects, sessionStatus.agent_connected, operation, forceSummaries);
        return;
      }
      const selected = projects.find((item) => item.id === selectedProjectID) ?? null;
      if (!selected) {
        selectedProject.current = "";
        setSelectedProjectID("");
        const next = normalizeRoute({ view: "projects" });
        currentRoute.current = next;
        setRoute(next);
        window.history.replaceState({}, "", routeHref(next));
        patch({ ...workspacePatch(projects), message: "The selected project is unavailable. Choose another project." });
        return;
      }

      const [readiness, nodes, services, deployments, sessions, audit, support] = await loadProject(client, selected.id);
      if (!isCurrent(operation, selectedProjectID)) return;
      const records = services.services ?? [];
      const jobs = deployments.deployments ?? [];
      const foundation = await loadFoundation(client, selected.id, records, sessionStatus.agent_connected);
      if (!isCurrent(operation, selectedProjectID)) return;
      const streamPatch = await reconnect(client, selected.id, sessions.sessions ?? [], jobs);
      if (!isCurrent(operation, selectedProjectID)) return;
      const nodeStatuses = foundation.placement?.nodes.map((node) => normalizeStatus(node.status)) ?? [];
      const entry: ProjectSummaryEntry = {
        status: "ready",
        fetchedAt: Date.now(),
        environment: foundation.placement?.environments.find((item) => item.status === "active")?.name,
        runtimeStatus: foundation.sources.runtime !== "available" ? "unavailable"
          : nodeStatuses.includes("failed") ? "failed"
            : nodeStatuses.includes("degraded") ? "degraded"
              : nodeStatuses.includes("unavailable") ? "unavailable"
                : nodeStatuses.length && nodeStatuses.every((status) => status === "healthy") ? "healthy" : "unknown",
        summary: deriveProjectSummary({ project: selected, readiness, services: records, deployments: jobs, foundation }),
      };
      summaryCache.current.set(selected.id, entry);
      setProjectSummaries((current) => ({ ...current, [selected.id]: entry }));
      setSelectedProjectID(selected.id);
      patch({
        status: "ready",
        projects,
        project: selected,
        readiness,
        nodes,
        services: records,
        deployments: jobs,
        sessions: sessions.sessions ?? [],
        audit: audit.events ?? [],
        support,
        incidents: foundation.incidents,
        serviceDetail: currentRoute.current.view === "services" ? records.find((item) => item.id === currentRoute.current.service) ?? null : null,
        foundation,
        ...streamPatch,
      });
    } catch (error) {
      if (!isCurrent(operation, selectedProjectID)) return;
      const err = error as Error & { status?: number };
      if (err.status === 401 || err.status === 403) clearSensitive();
      patch({
        status: err.status === 401 || err.status === 403 ? "permission" : err.status ? "error" : "network",
        message: err.message,
      });
    }
  }

  useEffect(() => {
    const initial = parseRoute(window.location.search);
    queueMicrotask(() => {
      // URL state is the external source of truth for refresh/deep-link restoration.
      setRoute(initial);
      currentRoute.current = initial;
      if (!initial.projectID) { void enterWorkspace(initial, true); return; }
      const operation = ++generation.current;
      selectedProject.current = initial.projectID;
      setSelectedProjectID(initial.projectID);
      patch(clearProjectPatch("Loading project…"));
      void load(initial.projectID, operation);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    function restoreRoute() {
      const next = parseRoute(window.location.search);
      clearSensitive();
      setReview(null);
      if (next.projectID === selectedProject.current) {
        setRoute(next);
        currentRoute.current = next;
        patch({ serviceDetail: next.view === "services" ? state.services.find((item) => item.id === next.service) ?? null : null });
        return;
      }
      if (!next.projectID) {
        void enterWorkspace(next, true);
        return;
      }
      void selectProject(next.projectID, next, true);
    }
    window.addEventListener("popstate", restoreRoute);
    return () => window.removeEventListener("popstate", restoreRoute);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.projects, state.services]);

  useEffect(() => () => {
    if (revealTimer.current !== null) window.clearTimeout(revealTimer.current);
  }, []);

  function updateRoute(next: ConsoleRoute, replace = false) {
    currentRoute.current = next;
    setRoute(next);
    window.history[replace ? "replaceState" : "pushState"]({}, "", routeHref(next));
  }

  function navigate(request: Partial<ConsoleRoute>) {
    const next = normalizeRoute({ ...route, ...request, projectID: request.projectID ?? route.projectID });
    if (next.projectID !== selectedProject.current) {
      setReview(null);
      if (!next.projectID) {
        void enterWorkspace(next);
      } else {
        void selectProject(next.projectID, next);
      }
      return;
    }
    if (next.view !== route.view || next.tab !== route.tab || next.environment !== route.environment) {
      clearSensitive();
      setReview(null);
      patch({ serviceDetail: next.view === "services" ? state.serviceDetail : null });
    }
    updateRoute(next);
  }

  async function enterWorkspace(destination = normalizeRoute({ view: "projects" }), replace = false) {
    clearSensitive();
    setReview(null);
    const operation = ++generation.current;
    selectedProject.current = "";
    setSelectedProjectID("");
    updateRoute(destination, replace);
    patch(workspacePatch(state.projects));
    const sessionStatus = currentSession.current;
    if (sessionStatus) await loadProjectSummaries(state.projects, sessionStatus.agent_connected, operation);
    else await load("", operation);
  }

  async function selectProject(id: string, destination = normalizeRoute({ projectID: id }), replace = false) {
    if (!id) return;
    clearSensitive();
    const operation = ++generation.current;
    selectedProject.current = id;
    setSelectedProjectID(id);
    updateRoute(destination, replace);
    patch(clearProjectPatch("Switching project…"));
    const queued = switchQueue.current.catch(() => undefined).then(async () => {
      if (!isCurrent(operation, id)) return;
      try {
        await client.switchProject(id, crypto.randomUUID());
        if (!isCurrent(operation, id)) return;
        await load(id, operation);
      } catch (error) {
        loadError(error as Error & { status?: number }, operation, id);
      }
    });
    switchQueue.current = queued;
    await queued;
  }

  function loadError(error: Error & { status?: number }, operation = generation.current, id = selectedProject.current) {
    if (!isCurrent(operation, id)) return;
    patch({ status: error.status === 401 || error.status === 403 ? "permission" : "error", message: error.message });
  }

  async function refreshCurrentData() {
    const id = selectedProject.current;
    const operation = ++generation.current;
    await load(id, operation, true);
  }

  async function createProject(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const orgID = session?.org_id ?? "";
    const name = String(form.get("name") ?? "");
    const slug = String(form.get("slug") ?? "");
    reviewMutation(
      { project: `organization ${orgID}`, targetType: "project", targetID: slug, operation: "create", diff: [`name: ${name}`, `slug: ${slug}`], risk: "Creates a durable Cloud project." },
      async (key) => {
        patch({ busy: "project" });
        try {
          const created = await client.createProject(orgID, { name, slug }, key);
          formElement.reset();
          await selectProject(created.id);
          return `Project ${created.id} created by the Local backend.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function addServer(event: FormEvent<HTMLFormElement>, onCreated?: () => void | Promise<void>) {
    event.preventDefault();
    if (!currentProject) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const authMethod = String(form.get("auth_method"));
    const host = String(form.get("public_host") ?? "");
    const role = String(form.get("role") ?? "");
    const body: Record<string, unknown> = {
      role,
      public_host: host,
      ssh_port: authMethod === "command" ? 0 : Number(form.get("ssh_port")),
      ssh_username: authMethod === "command" ? "" : String(form.get("ssh_username") ?? ""),
      auth_method: authMethod,
    };
    formElement.reset();
    const command = authMethod === "command";
    reviewMutation(
      { project: currentProject.name, targetType: "server", targetID: host, operation: "bootstrap", diff: [`role: ${role}`, command ? "connection: one-time bootstrap command" : `connection: SSH ${authMethod}`, ...(command ? [] : [`ssh port: ${String(body.ssh_port || "not reported")}`])], risk: command ? "Issues one expiring, session-scoped bootstrap command." : "Starts the canonical bootstrap worker flow over SSH. The one-time credential is requested only at final confirmation.", credential: command ? undefined : { label: authMethod === "private_key" ? "SSH private key" : "SSH password", inputLabel: authMethod === "private_key" ? "One-time SSH private key" : "One-time SSH password" } },
      async (key, credential) => {
        if (!command && !credential) throw new Error("Enter the one-time credential again to submit this reviewed attempt.");
        const operation = generation.current;
        const request = command ? body : { ...body, [authMethod === "private_key" ? "ssh_private_key" : "ssh_password"]: credential };
        patch({ busy: "server" });
        try {
          const created = await client.createBootstrap(currentProject.id, request, key);
          if (created.bootstrap_command) patch({ bootstrapCommand: created.bootstrap_command, bootstrapCommandSessionID: created.id });
          const events = await client.bootstrapEvents(currentProject.id, created.id);
          if (isCurrent(operation, currentProject.id)) patch({ bootstrapEvents: events, bootstrapEventsSessionID: created.id });
          await load(currentProject.id, operation);
          await onCreated?.();
          return `Bootstrap ${created.id} accepted with status ${created.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function diagnostics(nodeID: string) {
    if (!currentProject) return;
    patch({ nodeDetail: await client.node(currentProject.id, nodeID) });
    navigate({ view: "infrastructure", tab: "nodes", node: nodeID });
  }

  function nodeAction(nodeID: string, action: "offline" | "drain" | "remove") {
    if (!currentProject) return;
    reviewMutation(
      { project: currentProject.name, targetType: "node", targetID: nodeID, operation: action, diff: [`node status action: ${action}`], risk: action === "remove" ? "Destructive: removes the node from project inventory after canonical checks." : "May stop scheduling or mark runtime capacity unavailable.", confirmation: action === "remove" ? nodeID : undefined },
      async (key) => {
        patch({ busy: `${action}-${nodeID}` });
        try {
          const updated = await client.nodeAction(currentProject.id, nodeID, action, key);
          await load(currentProject.id);
          return `Node ${nodeID} returned status ${updated.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function loadBootstrapEvents(sessionID: string) {
    if (!currentProject) return;
    const operation = generation.current;
    const events = await client.bootstrapEvents(currentProject.id, sessionID);
    if (isCurrent(operation, currentProject.id)) patch({ bootstrapEvents: events, bootstrapEventsSessionID: sessionID });
  }

  async function refreshBootstrap(sessionID: string): Promise<BootstrapSession | undefined> {
    if (!currentProject) return;
    const project = currentProject;
    const operation = generation.current;
    const [sessions, events] = await Promise.all([client.bootstrapSessions(project.id), client.bootstrapEvents(project.id, sessionID)]);
    if (!isCurrent(operation, project.id)) return;
    const records = sessions.sessions ?? [];
    patch({ sessions: records, bootstrapEvents: events, bootstrapEventsSessionID: sessionID });
    const updated = records.find((session) => session.id === sessionID);
    if (terminalBootstrap(updated)) await load(project.id, operation);
    return updated;
  }

  function retryBootstrap(sessionID: string, onRetried?: () => void | Promise<void>) {
    if (!currentProject) return;
    reviewMutation(
      { project: currentProject.name, targetType: "bootstrap session", targetID: sessionID, operation: "retry", diff: ["resume the same durable checkpoint"], risk: "Retries only a retryable/dead-letter bootstrap session." },
      async (key) => {
        const updated = await client.retryBootstrap(currentProject.id, sessionID, key);
        if (updated.bootstrap_command) patch({ bootstrapCommand: updated.bootstrap_command, bootstrapCommandSessionID: updated.id });
        await load(currentProject.id);
        await onRetried?.();
        return `Bootstrap ${sessionID} returned status ${updated.status}.`;
      },
    );
  }

  async function loadDeploymentEvents(deploymentID: string) {
    if (!currentProject) return;
    const events = await client.deploymentEvents(currentProject.id, deploymentID);
    patch({ deploymentEvents: events.events ?? [] });
  }

  function rollback(deploymentID: string) {
    if (!currentProject) return;
    const source = state.deployments.find((deployment) => deployment.id === deploymentID);
    if (!source?.rollback_eligible) return;
    const currentDigest = source.current_digest || source.terminal_result?.current_digest || source.desired_digest || source.snapshot?.image.digest || "not reported";
    const previousDigest = source.previous_digest || source.terminal_result?.previous_digest || "not reported";
    reviewMutation(
      { project: currentProject.name, targetType: "deployment", targetID: deploymentID, operation: "rollback", diff: [`current digest: ${currentDigest}`, `previous known-good digest: ${previousDigest}`], risk: "Destructive runtime mutation; availability can change.", confirmation: deploymentID },
      async (key) => {
        patch({ busy: `rollback-${deploymentID}` });
        try {
          let job = await client.rollback(currentProject.id, deploymentID, key);
          for (let attempt = 0; attempt < 120 && !terminalDeployment(job); attempt++) {
            await new Promise((resolve) => window.setTimeout(resolve, deploymentPollInterval));
            job = await client.deployment(currentProject.id, job.id);
          }
          if (!terminalDeployment(job)) throw new Error(`Rollback ${job.id} did not reach a terminal state within 10 minutes.`);
          await loadDeploymentEvents(job.id);
          await load(currentProject.id);
          return `Rollback job ${job.id} finished with state ${job.rollout_state || job.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function secretCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const name = String(form.get("name") ?? "");
    reviewMutation(
      { project: currentProject.name, targetType: "secret", targetID: name, operation: "create", diff: [`service: ${String(form.get("service_id") ?? "")}`, `namespace: ${String(form.get("namespace") ?? "")}`], risk: "Writes a secret through the authenticated Agent. Values are never returned." },
      async (key) => {
        clearSensitive();
        patch({ busy: "secret-create" });
        try {
          const result = await client.createSecret(currentProject.id, secretBody(new FormData(formElement)), key);
          formElement.reset();
          await load(currentProject.id);
          return `Secret ${name} ${result.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function secretReveal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const name = String(form.get("name") ?? "");
    reviewMutation(
      { project: currentProject.name, targetType: "secret", targetID: name, operation: "reveal", diff: [`service: ${String(form.get("service_id") ?? "")}`, "second factor supplied at submission"], risk: "Sensitive value is visible in this tab only until its backend TTL expires." },
      async (key) => {
        clearSensitive();
        patch({ busy: "secret-reveal" });
        try {
          const revealed = await client.revealSecret(currentProject.id, name, secretBody(new FormData(formElement)), key);
          patch({ secretReveal: revealed });
          revealTimer.current = window.setTimeout(clearSensitive, (revealed.ttl_seconds ?? 60) * 1000);
          setReview(null);
          formElement.reset();
          return `Secret ${name} revealed by the Agent; TTL ${revealed.ttl_seconds ?? 60}s.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function secretRotate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const name = String(form.get("name") ?? "");
    reviewMutation(
      { project: currentProject.name, targetType: "secret", targetID: name, operation: "rotate", diff: [`service: ${String(form.get("service_id") ?? "")}`, "second factor supplied at submission"], risk: "Replaces the Agent-managed value; existing consumers may need restart/reconciliation." },
      async (key) => {
        clearSensitive();
        patch({ busy: "secret-rotate" });
        try {
          const result = await client.rotateSecret(currentProject.id, name, secretBody(new FormData(formElement)), key);
          formElement.reset();
          await load(currentProject.id);
          return `Secret ${name} ${result.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  function setupTOTP() {
    if (!currentProject) return;
    reviewMutation(
      { project: currentProject.name, targetType: "project TOTP", targetID: currentProject.id, operation: "setup", diff: ["create a new Agent-local fallback secret"], risk: "The setup URI is sensitive and will be cleared after five minutes." },
      async (key) => {
        clearSensitive();
        const result = await client.setupTOTP(currentProject.id, key);
        patch({ totpSetup: result });
        revealTimer.current = window.setTimeout(clearSensitive, result.ttl_seconds * 1000);
        setReview(null);
        return `TOTP setup created by the Agent; TTL ${result.ttl_seconds}s.`;
      },
    );
  }

  return {
    active: routeLabel(route),
    route,
    session,
    projectSummaries,
    review: review
      ? (() => {
          const { submit, ...visible } = review;
          void submit;
          return visible;
        })()
      : null,
    navigate,
    setProjectID: (id: string) => {
      setReview(null);
      void selectProject(id);
    },
    setServiceDetail: (serviceDetail: ServiceRecord | null) => {
      patch({ serviceDetail });
      if (currentRoute.current.view === "services") updateRoute(normalizeRoute({ ...currentRoute.current, service: serviceDetail?.id ?? "" }), true);
    },
    reviewMutation,
    closeReview,
    submitReview,
    state,
    actions: {
      addServer,
      createProject,
      diagnostics,
      load: refreshCurrentData,
      loadBootstrapEvents,
      refreshBootstrap,
      retryBootstrap,
      loadDeploymentEvents,
      hideSensitive: clearSensitive,
      nodeAction,
      rollback,
      secretCreate,
      secretReveal,
      secretRotate,
      setupTOTP,
    },
  } as ConsoleController;
}
