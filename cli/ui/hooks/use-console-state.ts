"use client";

import { FormEvent, useEffect, useMemo, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type {
  BootstrapSession,
  ConsoleState,
  DeploymentJob,
  NodeDiagnostics,
  NodeRecord,
  Project,
  ServiceRecord,
  TimelineEvent,
} from "@/lib/contracts/registry";

export function useConsoleState() {
  const [orgID, setOrgID] = useState("org-1");
  const [active, setActive] = useState("Projects");
  const [projectID, setProjectID] = useState("");
  const [state, setState] = useState<ConsoleState>({
    status: "idle",
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
    incidents: [],
    incidentDetail: null,
    incidentError: "",
    nodeDetail: null,
    serviceDetail: null,
    busy: "",
  });

  const client = useMemo(() => new LocalClient(), []);
  const currentProject = state.projects.find((item) => item.id === projectID) ?? state.projects[0] ?? null;

  function patch(value: Partial<ConsoleState>) {
    setState((prev) => ({ ...prev, ...value }));
  }

  async function load() {
    patch({ status: "loading", message: "" });
    try {
      const session = await client.session();
      if (!session.authenticated) {
        patch({
          status: "permission",
          message: "Sign in with GitHub to load your Opsi projects.",
          projects: [],
          project: null,
        });
        return;
      }
      const effectiveOrgID = session.org_id || orgID;
      if (session.org_id && session.org_id !== orgID) setOrgID(session.org_id);
      const effectiveProjectID = projectID || session.project_id || "";
      const list = await client.projects(effectiveOrgID);
      const projects = list.projects ?? [];
      const selected = projects.find((item) => item.id === effectiveProjectID) ?? projects[0] ?? null;
      if (!selected) {
        patch(emptyPatch(projects));
        return;
      }

      const [readiness, nodes, services, deployments, sessions, audit, support] = await loadProject(client, selected.id);
      const streamPatch = await reconnect(client, selected.id, sessions.sessions ?? [], deployments.deployments ?? []);
      setProjectID(selected.id);
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
        ...streamPatch,
      });
    } catch (error) {
      const err = error as Error & { status?: number };
      patch({
        status: err.status === 401 || err.status === 403 ? "permission" : err.status ? "error" : "network",
        message: err.message,
      });
    }
  }

  useEffect(() => {
    queueMicrotask(() => void load());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function createProject(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    patch({ busy: "project" });
    try {
      await client.createProject(orgID, { name: form.get("name"), slug: form.get("slug") });
      event.currentTarget.reset();
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function addServer(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    const authMethod = String(form.get("auth_method"));
    const secret = String(form.get("secret") ?? "");
    const body: Record<string, unknown> = {
      role: form.get("role"),
      public_host: form.get("public_host"),
      ssh_port: Number(form.get("ssh_port") || 22),
      ssh_username: form.get("ssh_username"),
      auth_method: authMethod,
    };
    body[authMethod === "private_key" ? "ssh_private_key" : "ssh_password"] = secret;
    patch({ busy: "server" });
    try {
      const session = await client.createBootstrap(currentProject.id, body);
      event.currentTarget.reset();
      patch({ bootstrapEvents: await client.bootstrapEvents(currentProject.id, session.id) });
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function createService(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    patch({ busy: "service" });
    try {
      await client.createService(currentProject.id, {
        name: form.get("name"),
        type: form.get("type"),
        source_type: form.get("source_type"),
        repo_url: form.get("repo_url"),
        image: form.get("image"),
        branch: form.get("branch"),
        git_sha: form.get("git_sha"),
        build_method: form.get("build_method"),
        build_context: form.get("build_context"),
        dockerfile: form.get("dockerfile"),
        manifest_path: form.get("manifest_path"),
        container_port: Number(form.get("container_port") || 0),
        health_path: form.get("health_path"),
        replicas: Number(form.get("replicas") || 1),
      });
      event.currentTarget.reset();
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function deploy(serviceID: string) {
    if (!currentProject || !state.readiness?.can_deploy) return;
    patch({ busy: `deploy-${serviceID}` });
    try {
      const job = await client.deploy(currentProject.id, serviceID);
      await loadDeploymentEvents(job.id);
      setActive("Deployments");
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function diagnostics(nodeID: string) {
    if (!currentProject) return;
    patch({ nodeDetail: await client.node(currentProject.id, nodeID) });
    setActive("Servers / Nodes");
  }

  async function nodeAction(nodeID: string, action: "drain" | "remove") {
    if (!currentProject) return;
    patch({ busy: `${action}-${nodeID}` });
    try {
      await client.nodeAction(currentProject.id, nodeID, action);
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function loadBootstrapEvents(sessionID: string) {
    if (!currentProject) return;
    patch({ bootstrapEvents: await client.bootstrapEvents(currentProject.id, sessionID) });
  }

  async function loadDeploymentEvents(deploymentID: string) {
    if (!currentProject) return;
    const events = await client.deploymentEvents(currentProject.id, deploymentID);
    patch({ deploymentEvents: events.events ?? [] });
  }

  async function rollback(deploymentID: string) {
    if (!currentProject) return;
    patch({ busy: `rollback-${deploymentID}` });
    try {
      const job = await client.rollback(currentProject.id, deploymentID);
      await loadDeploymentEvents(job.id);
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function secretCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    patch({ busy: "secret-create", secretReveal: null });
    try {
      await client.createSecret(currentProject.id, secretBody(form));
      event.currentTarget.reset();
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function secretReveal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    const name = String(form.get("name") ?? "");
    patch({ busy: "secret-reveal", secretReveal: null });
    try {
      const revealed = await client.revealSecret(currentProject.id, name, secretBody(form));
      patch({ secretReveal: revealed });
      window.setTimeout(() => patch({ secretReveal: null }), (revealed.ttl_seconds ?? 60) * 1000);
      event.currentTarget.reset();
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function secretRotate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject) return;
    const form = new FormData(event.currentTarget);
    const name = String(form.get("name") ?? "");
    patch({ busy: "secret-rotate", secretReveal: null });
    try {
      await client.rotateSecret(currentProject.id, name, secretBody(form));
      event.currentTarget.reset();
      await load();
    } finally {
      patch({ busy: "" });
    }
  }

  async function incidentList(event: FormEvent<HTMLFormElement>) {
	 event.preventDefault();
	 if (!currentProject) return;
	 const form = new FormData(event.currentTarget);
	 patch({ busy: "incident-list", incidentDetail: null, incidentError: "" });
	 try {
	   const result = await client.incidents(
	     currentProject.id,
	     String(form.get("user_id") ?? ""),
	     String(form.get("role") ?? ""),
	     String(form.get("status") ?? ""),
	   );
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
	   const result = await client.incident(
	     currentProject.id,
	     incidentID,
	     String(form.get("user_id") ?? ""),
	     String(form.get("role") ?? ""),
	   );
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
	 patch({ busy: "incident-resolve", incidentError: "" });
	 try {
	   const result = await client.resolveIncident(currentProject.id, incidentID, incidentBody(form));
	   patch({
	     incidentDetail: result.incident,
	     incidents: state.incidents.map((item) => (item.incident_id === result.incident.incident_id ? result.incident : item)),
	   });
	 } catch (error) {
	   patch({ incidentError: (error as Error).message });
	 } finally {
      patch({ busy: "" });
    }
  }

  return {
    active,
    orgID,
    setActive,
    setOrgID,
    setProjectID: (id: string) => {
      setProjectID(id);
      patch({ incidents: [], incidentDetail: null, incidentError: "" });
    },
    setServiceDetail: (serviceDetail: ServiceRecord | null) => patch({ serviceDetail }),
    state: { ...state, project: currentProject },
    actions: {
      addServer,
      createProject,
      createService,
      deploy,
      diagnostics,
      load,
      loadBootstrapEvents,
      loadDeploymentEvents,
      incidentList,
      incidentGet,
      incidentResolve,
      nodeAction,
      rollback,
      secretCreate,
      secretReveal,
      secretRotate,
    },
  };
}

function secretBody(form: FormData) {
  return {
    service_id: form.get("service_id"),
    name: form.get("name"),
    namespace: form.get("namespace"),
    user_id: form.get("user_id"),
    role: form.get("role"),
    otp_request_id: form.get("otp_request_id"),
    otp_code: form.get("otp_code"),
    totp_code: form.get("totp_code"),
  };
}

function incidentBody(form: FormData) {
  return {
    user_id: form.get("user_id"),
    role: form.get("role"),
  };
}

async function loadProject(client: LocalClient, projectID: string) {
  return Promise.all([
    client.readiness(projectID),
    client.nodes(projectID),
    client.services(projectID),
    client.deployments(projectID),
    client.bootstrapSessions(projectID),
    client.audit(projectID),
    client.support(projectID),
  ]);
}

async function reconnect(
  client: LocalClient,
  projectID: string,
  sessions: BootstrapSession[],
  deployments: DeploymentJob[],
): Promise<Pick<ConsoleState, "bootstrapEvents" | "deploymentEvents">> {
  const activeSession = sessions.find((item) => ["created", "preflight", "installing", "waiting_agent"].includes(item.status)) ?? sessions[0];
  const bootstrapEvents = activeSession ? await client.bootstrapEvents(projectID, activeSession.id) : [];
  const deploymentEvents = deployments[0] ? (await client.deploymentEvents(projectID, deployments[0].id)).events ?? [] : [];
  return { bootstrapEvents, deploymentEvents };
}

function emptyPatch(projects: Project[]): Partial<ConsoleState> {
  return {
    status: "empty",
    projects,
    project: null,
    readiness: null,
    nodes: [] as NodeRecord[],
    services: [] as ServiceRecord[],
    deployments: [] as DeploymentJob[],
    sessions: [] as BootstrapSession[],
    audit: [],
    support: null,
    incidents: [],
    incidentDetail: null,
    incidentError: "",
    bootstrapEvents: [] as TimelineEvent[],
    deploymentEvents: [] as TimelineEvent[],
    nodeDetail: null as NodeDiagnostics | null,
  };
}
