export const projectDestinations = [
  { id: "topology", label: "Topology" },
  { id: "overview", label: "Overview" },
  { id: "services", label: "Services" },
  { id: "infrastructure", label: "Infrastructure" },
  { id: "delivery", label: "Delivery" },
  { id: "observability", label: "Observability" },
  { id: "security", label: "Security" },
] as const;

export const groupedTabs = {
  delivery: [
    { id: "pipeline", label: "Pipeline" },
    { id: "builds", label: "Builds" },
    { id: "deployments", label: "Deployments" },
    { id: "exposure", label: "Exposure" },
    { id: "source", label: "Source" },
  ],
  infrastructure: [
    { id: "servers", label: "Servers" },
    { id: "resources", label: "Managed Resources" },
    { id: "storage", label: "Retained Storage" },
  ],
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
export type ConsoleView = ProjectView | "home" | "projects" | "settings";
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
};

const projectViews = new Set<ConsoleView>(projectDestinations.map((item) => item.id));

export function isProjectView(view: ConsoleView): view is ProjectView {
  return projectViews.has(view);
}

export function defaultTab(view: ConsoleView) {
  return view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs][0].id : "";
}

const legacyTabs: Record<string, string[]> = {
  services: ["overview", "dependencies", "source", "builds", "runtime"],
  infrastructure: ["topology", "runtimes", "nodes", "bootstrap"],
  observability: ["health", "metrics", "logs", "incidents"],
  security: ["secrets"],
};

export function normalizeRoute(route: Partial<ConsoleRoute>): ConsoleRoute {
  const projectID = route.projectID ?? "";
  let view = route.view ?? (projectID ? "topology" : "home");
  if (!projectID && isProjectView(view)) view = "projects";
  if (view === "home" || view === "projects") return { projectID: "", view, tab: "" };

  // Preserve backward compatibility for legacy links/tests
  if (view === "infrastructure" && route.tab === "topology") {
    return {
      projectID,
      view: "topology",
      tab: "",
      ...compactViewState("topology", route),
    };
  }

  const tabs = view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs] : [];
  const tab = route.tab ?? "";
  const isKnownTab = tabs.some((item) => item.id === tab) || (legacyTabs[view]?.includes(tab) ?? false);
  const validTab = isKnownTab ? tab : defaultTab(view);

  return { projectID, view, tab: validTab, ...compactViewState(view, route) };
}

export function parseRoute(search: string): ConsoleRoute {
  const params = new URLSearchParams(search);
  const requested = params.get("view") as ConsoleView | null;
  const valid = requested === "home" || requested === "projects" || requested === "settings" || projectViews.has(requested as ConsoleView);
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
  return `/?${params}`;
}

function compactViewState(view: ConsoleView, route: Partial<ConsoleRoute>) {
  const state: Partial<ConsoleRoute> = route.environment ? { environment: route.environment } : {};
  const keys = view === "delivery"
    ? ["service", "build", "deployment", "status", "kind", "repository", "sha", "cursor"] as const
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
