export const projectDestinations = [
  { id: "deploy", label: "Deploy" },
  { id: "observability", label: "Observability" },
  { id: "security", label: "Security" },
] as const;

export const groupedTabs = {
  observability: [
    { id: "overview", label: "Overview" },
    { id: "applications", label: "Applications" },
    { id: "servers", label: "Servers" },
    { id: "resources", label: "Managed Resources" },
  ],
  security: [
    { id: "overview", label: "Overview" },
    { id: "audit", label: "Audit" },
    { id: "access", label: "Access & Identities" },
  ],
  settings: [
    { id: "general", label: "General" },
    { id: "authentication", label: "Authentication" },
    { id: "integrations", label: "Integrations" },
    { id: "system", label: "System" },
  ],
} as const;

export type ProjectView = (typeof projectDestinations)[number]["id"];
export type LegacyProjectView = "topology" | "overview" | "services" | "infrastructure" | "delivery";
export type ConsoleView = ProjectView | LegacyProjectView | "home" | "projects" | "settings";
export type ConsoleRoute = {
  projectID: string;
  view: ConsoleView;
  tab: string;
  environment?: string;
  service?: string;
  build?: string;
  deployment?: string;
  status?: string;
  kind?: string;
  repository?: string;
  sha?: string;
  cursor?: string;
  runtime?: string;
  node?: string;
  session?: string;
  topology?: string;
  topologyMode?: string;
  resource?: string;
  server?: string;
  storage?: string;
  incident?: string;
  level?: string;
  query?: string;
  window?: string;
  sourceProject?: string;
  sourceInstallation?: string;
  sourceRepository?: string;
  sourceRef?: string;
  sourceHostname?: string;
};

const projectViews = new Set<ConsoleView>(projectDestinations.map((item) => item.id));
const legacyProjectViews = new Set<ConsoleView>(["topology", "overview", "services", "infrastructure", "delivery"]);

export function isProjectView(view: ConsoleView): view is ProjectView {
  return projectViews.has(view);
}

export function defaultTab(view: ConsoleView) {
  return view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs][0].id : "";
}

export function normalizeRoute(route: Partial<ConsoleRoute>): ConsoleRoute {
  const projectID = route.projectID ?? "";
  let view = route.view ?? (projectID ? "deploy" : "home");
  if (legacyProjectViews.has(view)) {
    return { projectID, view: projectID ? "deploy" : "projects", tab: "", ...compactViewState("deploy", route) };
  }
  if (!projectID && isProjectView(view)) view = "projects";
  if (view === "home" || view === "projects") return { projectID: "", view, tab: "" };

  const tabs = view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs] : [];
  const tab = route.tab ?? "";
  const isKnownTab = tabs.some((item) => item.id === tab);
  const validTab = isKnownTab ? tab : defaultTab(view);

  return { projectID, view, tab: validTab, ...compactViewState(view, route) };
}

export function parseRoute(search: string): ConsoleRoute {
  const params = new URLSearchParams(search);
  const requested = params.get("view") as ConsoleView | null;
  const valid = requested === "home" || requested === "projects" || requested === "settings" || projectViews.has(requested as ConsoleView) || legacyProjectViews.has(requested as ConsoleView);
  return normalizeRoute({
    projectID: params.get("project") ?? "",
    view: valid ? requested ?? undefined : undefined,
    tab: params.get("tab") ?? "",
    environment: params.get("environment") ?? "",
    service: params.get("service") ?? "",
    build: params.get("build") ?? "",
    deployment: params.get("deployment") ?? "",
    status: params.get("status") ?? "",
    kind: params.get("kind") ?? "",
    repository: params.get("repository") ?? "",
    sha: params.get("sha") ?? "",
    cursor: params.get("cursor") ?? "",
    runtime: params.get("runtime") ?? "",
    node: params.get("node") ?? "",
    session: params.get("session") ?? "",
    topology: params.get("topology") ?? "",
    topologyMode: params.get("topologyMode") ?? "",
    resource: params.get("resource") ?? "",
    server: params.get("server") ?? "",
    storage: params.get("storage") ?? "",
    incident: params.get("incident") ?? "",
    level: params.get("level") ?? "",
    query: params.get("query") ?? "",
    window: params.get("window") ?? "",
    sourceProject: params.get("source_project") ?? "",
    sourceInstallation: params.get("source_installation") ?? "",
    sourceRepository: params.get("source_repository") ?? "",
    sourceRef: params.get("source_ref") ?? "",
    sourceHostname: params.get("source_hostname") ?? "",
  });
}

export function routeHref(route: Partial<ConsoleRoute>) {
  const normalized = normalizeRoute(route);
  const params = new URLSearchParams();
  if (normalized.projectID) params.set("project", normalized.projectID);
  params.set("view", normalized.view);
  if (normalized.tab) params.set("tab", normalized.tab);
  for (const key of [
    "environment",
    "service",
    "build",
    "deployment",
    "status",
    "kind",
    "repository",
    "sha",
    "cursor",
    "runtime",
    "node",
    "session",
    "topology",
    "topologyMode",
    "resource",
    "server",
    "storage",
    "incident",
    "level",
    "query",
    "window",
  ] as const) {
    if (normalized[key]) params.set(key, normalized[key]);
  }
  for (const [key, value] of [
    ["source_project", normalized.sourceProject],
    ["source_installation", normalized.sourceInstallation],
    ["source_repository", normalized.sourceRepository],
    ["source_ref", normalized.sourceRef],
    ["source_hostname", normalized.sourceHostname],
  ] as const) {
    if (value) params.set(key, value);
  }
  return `/?${params}`;
}

function compactViewState(view: ConsoleView, route: Partial<ConsoleRoute>) {
  const state: Partial<ConsoleRoute> = route.environment ? { environment: route.environment } : {};
  const keys = view === "deploy"
    ? ["service", "build", "deployment", "status", "kind", "repository", "sha", "cursor", "sourceProject", "sourceInstallation", "sourceRepository", "sourceRef", "sourceHostname"] as const
    : view === "topology"
      ? ["service", "topology", "topologyMode", "runtime", "node", "session"] as const
      : view === "infrastructure"
        ? ["runtime", "node", "session", "service", "topology", "topologyMode", "resource", "server", "storage"] as const
        : view === "observability"
          ? ["service", "server", "resource", "node", "incident", "status", "level", "query", "cursor", "window"] as const
          : view === "services"
            ? ["service"] as const
          : [] as const;
  for (const key of keys) {
    if (route[key]) state[key] = route[key];
  }
  return state;
}

export function routeLabel(route: ConsoleRoute) {
  if (route.tab && route.view in groupedTabs) {
    return groupedTabs[route.view as keyof typeof groupedTabs].find((item) => item.id === route.tab)?.label ?? route.view;
  }
  return projectDestinations.find((item) => item.id === route.view)?.label ?? (route.view === "home" ? "Home" : route.view === "projects" ? "Projects" : "Settings");
}
