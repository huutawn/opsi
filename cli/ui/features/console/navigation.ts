export const projectDestinations = [
  { id: "overview", label: "Overview" },
  { id: "services", label: "Services" },
  { id: "delivery", label: "Delivery" },
  { id: "infrastructure", label: "Infrastructure" },
  { id: "observability", label: "Observability" },
  { id: "security", label: "Security" },
] as const;

export const groupedTabs = {
  delivery: [
    { id: "source", label: "Source" },
    { id: "builds", label: "Build Records" },
    { id: "deployments", label: "Deployments" },
    { id: "exposure", label: "Exposure" },
  ],
  infrastructure: [
    { id: "runtime", label: "Runtime" },
    { id: "nodes", label: "Nodes / Servers" },
    { id: "bootstrap", label: "Bootstrap" },
    { id: "topology", label: "Topology" },
  ],
  observability: [
    { id: "health", label: "Health" },
    { id: "metrics", label: "Metrics" },
    { id: "logs", label: "Logs" },
    { id: "incidents", label: "Incidents" },
    { id: "support", label: "Support" },
  ],
  security: [
    { id: "secrets", label: "Secrets" },
    { id: "audit", label: "Audit" },
  ],
} as const;

export type ProjectView = (typeof projectDestinations)[number]["id"];
export type ConsoleView = ProjectView | "projects" | "settings";
export type ConsoleRoute = { projectID: string; view: ConsoleView; tab: string };

const projectViews = new Set<ConsoleView>(projectDestinations.map((item) => item.id));

export function isProjectView(view: ConsoleView): view is ProjectView {
  return projectViews.has(view);
}

export function defaultTab(view: ConsoleView) {
  return view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs][0].id : "";
}

export function normalizeRoute(route: Partial<ConsoleRoute>): ConsoleRoute {
  const projectID = route.projectID ?? "";
  let view = route.view ?? (projectID ? "overview" : "projects");
  if (!projectID && isProjectView(view)) view = "projects";
  if (view === "projects") return { projectID: "", view, tab: "" };
  const tabs = view in groupedTabs ? groupedTabs[view as keyof typeof groupedTabs] : [];
  const tab = tabs.some((item) => item.id === route.tab) ? route.tab ?? "" : defaultTab(view);
  return { projectID, view, tab };
}

export function parseRoute(search: string): ConsoleRoute {
  const params = new URLSearchParams(search);
  const requested = params.get("view") as ConsoleView | null;
  const valid = requested === "projects" || requested === "settings" || projectViews.has(requested as ConsoleView);
  return normalizeRoute({
    projectID: params.get("project") ?? "",
    view: valid ? requested ?? undefined : undefined,
    tab: params.get("tab") ?? "",
  });
}

export function routeHref(route: Partial<ConsoleRoute>) {
  const normalized = normalizeRoute(route);
  const params = new URLSearchParams();
  if (normalized.projectID) params.set("project", normalized.projectID);
  params.set("view", normalized.view);
  if (normalized.tab) params.set("tab", normalized.tab);
  return `/?${params}`;
}

export function routeLabel(route: ConsoleRoute) {
  if (route.tab && route.view in groupedTabs) {
    return groupedTabs[route.view as keyof typeof groupedTabs].find((item) => item.id === route.tab)?.label ?? route.view;
  }
  return projectDestinations.find((item) => item.id === route.view)?.label ?? (route.view === "projects" ? "Projects" : "Settings");
}

export function routeForLegacy(label: string, projectID: string): ConsoleRoute {
  const routes: Record<string, Partial<ConsoleRoute>> = {
    Projects: { projectID: "", view: "projects" },
    Overview: { view: "overview" },
    Services: { view: "services" },
    GitHub: { view: "delivery", tab: "source" },
    "Build Records": { view: "delivery", tab: "builds" },
    Deployments: { view: "delivery", tab: "deployments" },
    Exposure: { view: "delivery", tab: "exposure" },
    Runtime: { view: "infrastructure", tab: "runtime" },
    "Servers / Nodes": { view: "infrastructure", tab: "nodes" },
    Bootstrap: { view: "infrastructure", tab: "bootstrap" },
    Topology: { view: "infrastructure", tab: "topology" },
    Health: { view: "observability", tab: "health" },
    Metrics: { view: "observability", tab: "metrics" },
    Logs: { view: "observability", tab: "logs" },
    Incidents: { view: "observability", tab: "incidents" },
    Support: { view: "observability", tab: "support" },
    Secrets: { view: "security", tab: "secrets" },
    Audit: { view: "security", tab: "audit" },
    Settings: { view: "settings" },
  };
  return normalizeRoute({ projectID, ...(routes[label] ?? { view: "overview" }) });
}
