"use client";

import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { LocalAPIError, LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { ConsoleController, MutationRequest, MutationReview } from "@/features/console/types";
import { normalizeRoute, parseRoute, routeHref, routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { deriveProjectSummary, emptyFoundation, normalizeStatus, type FoundationState, type ProjectSummaryEntry } from "@/lib/presentation/project";
import type { ConsoleState, ServiceRecord } from "@/lib/contracts/registry";
import { clearProjectPatch, loadFoundation, loadProject, loadProjectSummary, reconnect, secretBody, workspacePatch } from "@/hooks/console-state-support";

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
    bootstrapEvents: [],
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

  async function loadProjectSummaries(projects: typeof state.projects, agentStatus: string, operation: number) {
    const pending = projects.filter((project) => !summaryCache.current.has(project.id));
    const cached = Object.fromEntries(projects.flatMap((project) => {
      const entry = summaryCache.current.get(project.id);
      return entry ? [[project.id, entry]] : [];
    }));
    if (isCurrent(operation, "")) {
      setProjectSummaries((current) => ({
        ...current,
        ...cached,
        ...Object.fromEntries(pending.map((project) => [project.id, { status: "loading" as const }])),
      }));
    }
    let cursor = 0;
    await Promise.all(Array.from({ length: Math.min(2, pending.length) }, async () => {
      for (;;) {
        const project = pending[cursor++];
        if (!project) return;
        try {
          const entry = await loadProjectSummary(client, project, agentStatus);
          if (!isCurrent(operation, "")) return;
          summaryCache.current.set(project.id, entry);
          setProjectSummaries((current) => ({ ...current, [project.id]: entry }));
        } catch (cause) {
          if (!isCurrent(operation, "")) return;
          setProjectSummaries((current) => ({ ...current, [project.id]: { status: "error", error: (cause as Error).message } }));
        }
      }
    }));
  }

  async function load(selectedProjectID = selectedProject.current, operation = generation.current) {
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
        void loadProjectSummaries(projects, sessionStatus.agent_connected, operation);
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
    // URL state is the external source of truth for refresh/deep-link restoration.
    setRoute(initial);
    currentRoute.current = initial;
    queueMicrotask(() => {
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
    if (next.view !== route.view || next.tab !== route.tab) {
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

  async function selectProject(id: string, destination = normalizeRoute({ projectID: id, view: "overview" }), replace = false) {
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
          await load("", generation.current);
          return `Project ${created.id} created by the Local backend.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function addServer(event: FormEvent<HTMLFormElement>) {
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
      ssh_port: Number(form.get("ssh_port")),
      ssh_username: String(form.get("ssh_username") ?? ""),
      auth_method: authMethod,
    };
    formElement.reset();
    reviewMutation(
      { project: currentProject.name, targetType: "server", targetID: host, operation: "bootstrap", diff: [`role: ${role}`, `auth: ${authMethod}`, `ssh port: ${String(body.ssh_port || "not reported")}`], risk: "Starts the canonical bootstrap worker flow. The one-time credential is requested only at final confirmation.", credential: { label: authMethod === "private_key" ? "SSH private key" : "SSH password", inputLabel: authMethod === "private_key" ? "One-time SSH private key" : "One-time SSH password" } },
      async (key, credential) => {
        if (!credential) throw new Error("Enter the one-time credential again to submit this reviewed attempt.");
        const operation = generation.current;
        const request = { ...body, [authMethod === "private_key" ? "ssh_private_key" : "ssh_password"]: credential };
        patch({ busy: "server" });
        try {
          const created = await client.createBootstrap(currentProject.id, request, key);
          const events = await client.bootstrapEvents(currentProject.id, created.id);
          if (isCurrent(operation, currentProject.id)) patch({ bootstrapEvents: events });
          await load(currentProject.id, operation);
          return `Bootstrap ${created.id} accepted with status ${created.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  async function createService(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const body = {
      name: form.get("name"), type: form.get("type"), source_type: "image",
      container_port: Number(form.get("container_port") || 0), health_path: form.get("health_path"), replicas: Number(form.get("replicas") || 1),
    };
    reviewMutation(
      { project: currentProject.name, targetType: "service", targetID: String(body.name ?? ""), operation: "create catalog entry", diff: [`type: ${String(body.type ?? "")}`, `port: ${body.container_port || "unset"}`, `replicas: ${body.replicas}`], risk: "Creates service identity only. Source binding and immutable BuildRecords remain separate." },
      async (key) => {
        patch({ busy: "service" });
        try {
          const created = await client.createService(currentProject.id, body, key);
          formElement.reset();
          await load(currentProject.id);
          return `Service ${created.id} created with status ${created.status}.`;
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
    patch({ bootstrapEvents: await client.bootstrapEvents(currentProject.id, sessionID) });
  }

  function retryBootstrap(sessionID: string) {
    if (!currentProject) return;
    reviewMutation(
      { project: currentProject.name, targetType: "bootstrap session", targetID: sessionID, operation: "retry", diff: ["resume the same durable checkpoint"], risk: "Retries only a retryable/dead-letter bootstrap session." },
      async (key) => {
        const updated = await client.retryBootstrap(currentProject.id, sessionID, key);
        await load(currentProject.id);
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
    reviewMutation(
      { project: currentProject.name, targetType: "deployment", targetID: deploymentID, operation: "rollback", diff: ["restore the exact previous Agent known-good snapshot"], risk: "Destructive runtime mutation; availability can change.", confirmation: deploymentID },
      async (key) => {
        patch({ busy: `rollback-${deploymentID}` });
        try {
          const job = await client.rollback(currentProject.id, deploymentID, key);
          await loadDeploymentEvents(job.id);
          await load(currentProject.id);
          return `Rollback job ${job.id} accepted with status ${job.status}.`;
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
      createService,
      diagnostics,
      load,
      loadBootstrapEvents,
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
