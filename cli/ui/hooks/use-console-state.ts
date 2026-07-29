"use client";

import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { LocalAPIError, LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { MutationRequest, MutationReview } from "@/features/console/types";
import { normalizeRoute, parseRoute, routeForLegacy, routeHref, routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { emptyFoundation, type FoundationState } from "@/lib/presentation/project";
import type { ConsoleState, ServiceRecord } from "@/lib/contracts/registry";
import { clearProjectPatch, loadFoundation, loadProject, reconnect, secretBody, workspacePatch } from "@/hooks/console-state-support";

export function useConsoleState() {
  const [session, setSession] = useState<LocalSessionStatus | null>(null);
  const [route, setRoute] = useState<ConsoleRoute>({ projectID: "", view: "projects", tab: "" });
  const [projectID, setSelectedProjectID] = useState("");
  const [review, setReview] = useState<(MutationReview & { submit: (key: string) => Promise<string> }) | null>(null);
  const revealTimer = useRef<number | null>(null);
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
    incidentDetail: null,
    incidentError: "",
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

  function reviewMutation(request: MutationRequest, submit: (key: string) => Promise<string>) {
    setReview({ ...request, idempotencyKey: crypto.randomUUID(), status: "reviewing", submit });
  }

  async function submitReview() {
    if (!review || review.status === "submitting") return;
    setReview((current) => (current ? { ...current, status: "submitting", error: "", nextAction: "" } : current));
    try {
      const evidence = await review.submit(review.idempotencyKey);
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

  async function load(selectedProjectID = projectID) {
    patch(state.status === "ready" ? { message: "" } : { status: "loading", message: "" });
    try {
      const sessionStatus = await client.session(selectedProjectID || undefined);
      setSession(sessionStatus);
      if (!sessionStatus.authenticated) {
        clearSensitive();
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
      const projects = list.projects ?? [];
      if (!selectedProjectID) {
        patch(workspacePatch(projects));
        return;
      }
      const selected = projects.find((item) => item.id === selectedProjectID) ?? null;
      if (!selected) {
        setSelectedProjectID("");
        const next = normalizeRoute({ view: "projects" });
        setRoute(next);
        window.history.replaceState({}, "", routeHref(next));
        patch({ ...workspacePatch(projects), message: "The selected project is unavailable. Choose another project." });
        return;
      }

      const [readiness, nodes, services, deployments, sessions, audit, support] = await loadProject(client, selected.id);
      const foundation = await loadFoundation(client, selected.id, services.services ?? [], sessionStatus.agent_connected);
      const streamPatch = await reconnect(client, selected.id, sessions.sessions ?? [], deployments.deployments ?? []);
      setSelectedProjectID(selected.id);
      patch({
        status: "ready",
        projects,
        project: selected,
        readiness,
        nodes,
        services: services.services ?? [],
        deployments: deployments.deployments ?? [],
        sessions: sessions.sessions ?? [],
        audit: audit.events ?? [],
        support,
        incidents: foundation.incidents,
        foundation,
        ...streamPatch,
      });
    } catch (error) {
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
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRoute(initial);
    setSelectedProjectID(initial.projectID);
    queueMicrotask(() => void load(initial.projectID));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    function restoreRoute() {
      const next = parseRoute(window.location.search);
      clearSensitive();
      setReview(null);
      setRoute(next);
      if (next.projectID === projectID) return;
      setSelectedProjectID(next.projectID);
      if (!next.projectID) {
        patch(workspacePatch(state.projects));
        return;
      }
      patch(clearProjectPatch("Restoring project…"));
      void client.switchProject(next.projectID, crypto.randomUUID()).then(() => load(next.projectID)).catch(loadError);
    }
    window.addEventListener("popstate", restoreRoute);
    return () => window.removeEventListener("popstate", restoreRoute);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectID, state.projects]);

  useEffect(
    () => () => {
      if (revealTimer.current !== null) window.clearTimeout(revealTimer.current);
    },
    [],
  );

  function updateRoute(next: ConsoleRoute, replace = false) {
    setRoute(next);
    window.history[replace ? "replaceState" : "pushState"]({}, "", routeHref(next));
  }

  function navigate(request: Partial<ConsoleRoute>) {
    const next = normalizeRoute({ ...route, ...request, projectID: request.projectID ?? route.projectID });
    if (next.projectID !== projectID) {
      setReview(null);
      if (!next.projectID) {
        clearSensitive();
        setReview(null);
        setSelectedProjectID("");
        updateRoute(next);
        patch(workspacePatch(state.projects));
      } else {
        void selectProject(next.projectID, next);
      }
      return;
    }
    if (next.view !== route.view || next.tab !== route.tab) {
      clearSensitive();
      patch({ serviceDetail: next.view === "services" ? state.serviceDetail : null });
    }
    updateRoute(next);
  }

  async function selectProject(id: string, destination = normalizeRoute({ projectID: id, view: "overview" })) {
    if (!id) return;
    clearSensitive();
    setSelectedProjectID(id);
    updateRoute(destination);
    patch(clearProjectPatch("Switching project…"));
    try {
      await client.switchProject(id, crypto.randomUUID());
      await load(id);
    } catch (error) {
      loadError(error as Error & { status?: number });
    }
  }

  function loadError(error: Error & { status?: number }) {
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
          await selectProject(created.id);
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
    reviewMutation(
      { project: currentProject.name, targetType: "server", targetID: host, operation: "bootstrap", diff: [`role: ${role}`, `auth: ${authMethod}`, `ssh port: ${String(form.get("ssh_port") || 22)}`], risk: "Starts the canonical bootstrap worker flow. Credentials are submitted once and stay out of browser storage." },
      async (key) => {
        const submitted = new FormData(formElement);
        const submittedAuth = String(submitted.get("auth_method"));
        const body: Record<string, unknown> = {
          role: submitted.get("role"), public_host: submitted.get("public_host"),
          ssh_port: Number(submitted.get("ssh_port") || 22), ssh_username: submitted.get("ssh_username"), auth_method: submittedAuth,
        };
        body[submittedAuth === "private_key" ? "ssh_private_key" : "ssh_password"] = String(submitted.get("secret") ?? "");
        patch({ busy: "server" });
        try {
          const created = await client.createBootstrap(currentProject.id, body, key);
          formElement.reset();
          patch({ bootstrapEvents: await client.bootstrapEvents(currentProject.id, created.id) });
          await load(currentProject.id);
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
    navigate(routeForLegacy("Servers / Nodes", currentProject.id));
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
      { project: currentProject.name, targetType: "secret", targetID: name, operation: "create", diff: [`service: ${String(form.get("service_id") ?? "")}`, `namespace: ${String(form.get("namespace") || "default")}`], risk: "Writes a secret through the authenticated Agent. Values are never returned." },
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
        return `TOTP setup created by the Agent; TTL ${result.ttl_seconds}s.`;
      },
    );
  }

  async function incidentList(event: FormEvent<HTMLFormElement>) {
	 event.preventDefault();
	 if (!currentProject) return;
	 const form = new FormData(event.currentTarget);
	 patch({ busy: "incident-list", incidentDetail: null, incidentError: "" });
	 try {
	   const result = await client.incidents(currentProject.id, String(form.get("status") ?? ""));
	   patch({ incidents: result.incidents ?? [] });
	 } catch (error) {
	   patch({ incidentError: (error as Error).message });
	 } finally {
	   patch({ busy: "" });
	 }
  }

  async function incidentGet(event: FormEvent<HTMLFormElement>) {
	 event.preventDefault();
	 if (!currentProject) return;
	 const form = new FormData(event.currentTarget);
	 const incidentID = String(form.get("incident_id") ?? "");
	 patch({ busy: "incident-get", incidentDetail: null, incidentError: "" });
	 try {
	   const result = await client.incident(currentProject.id, incidentID);
	   patch({ incidentDetail: result.incident });
	 } catch (error) {
	   patch({ incidentError: (error as Error).message });
	 } finally {
	   patch({ busy: "" });
    }
  }

  async function incidentResolve(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    const incidentID = String(form.get("incident_id") ?? "");
    reviewMutation(
      { project: currentProject.name, targetType: "incident", targetID: incidentID, operation: "resolve", diff: ["mark factual Agent incident resolved"], risk: "Changes incident lifecycle state but does not execute remediation." },
      async (key) => {
        patch({ busy: "incident-resolve", incidentError: "" });
        try {
          const result = await client.resolveIncident(currentProject.id, incidentID, key);
          patch({ incidentDetail: result.incident, incidents: state.incidents.map((item) => (item.incident_id === result.incident.incident_id ? result.incident : item)) });
          return `Incident ${incidentID} returned status ${result.incident.status}.`;
        } finally {
          patch({ busy: "" });
        }
      },
    );
  }

  return {
    active: routeLabel(route),
    route,
    session,
    review: review
      ? (() => {
          const { submit, ...visible } = review;
          void submit;
          return visible;
        })()
      : null,
    navigate,
    setActive: (view: string) => navigate(routeForLegacy(view, projectID)),
    setProjectID: (id: string) => {
      setReview(null);
      void selectProject(id);
    },
    setServiceDetail: (serviceDetail: ServiceRecord | null) => patch({ serviceDetail }),
    reviewMutation,
    closeReview,
    submitReview,
    state: { ...state, project: currentProject },
    actions: {
      addServer,
      createProject,
      createService,
      diagnostics,
      load,
      loadBootstrapEvents,
      retryBootstrap,
      loadDeploymentEvents,
      incidentList,
      incidentGet,
      incidentResolve,
      nodeAction,
      rollback,
      secretCreate,
      secretReveal,
      secretRotate,
      setupTOTP,
    },
  };
}
