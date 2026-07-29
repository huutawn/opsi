import { AuditView } from "@/features/console/operations-views";
import { NodesView } from "@/features/console/nodes-view";
import { BuildRecordsView } from "@/features/build-records/build-records-view";
import { DeploymentsView as DeliveryDeploymentsView } from "@/features/deployments/deployments-view";
import { GitHubView } from "@/features/github/github-view";
import { IncidentsView } from "@/features/incidents/incidents-view";
import { LogsView } from "@/features/logs/logs-view";
import { MetricsView } from "@/features/metrics/metrics-view";
import { RuntimeView } from "@/features/runtime/runtime-view";
import { SecretsView } from "@/features/secrets/secrets-view";
import { SettingsView } from "@/features/settings/settings-view";
import { SupportView } from "@/features/support/support-view";
import { TopologyView as InfrastructureTopologyView } from "@/features/topology/topology-view";
import { groupedTabs, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import { OverviewView } from "@/features/overview/overview-view";
import { ProjectsView } from "@/features/projects/projects-view";
import { ServicesView } from "@/features/services/services-view";
import type { ConsoleController } from "@/features/console/types";

export const coreViewMap = { overview: OverviewView, services: ServicesView } as const;

const tabViewMap: Record<string, Record<string, (props: { console: ConsoleController }) => React.ReactNode>> = {
  delivery: { source: GitHubView, builds: BuildRecordsView, deployments: DeliveryDeploymentsView, exposure: DeliveryDeploymentsView },
  infrastructure: { runtime: RuntimeView, nodes: NodesView, bootstrap: NodesView, topology: InfrastructureTopologyView },
  observability: { health: MetricsView, metrics: MetricsView, logs: LogsView, incidents: IncidentsView, support: SupportView },
  security: { secrets: SecretsView, audit: AuditView },
};

export function routeView(route: ConsoleRoute, console: ConsoleController) {
  if (route.view === "projects") return <ProjectsView console={console} />;
  if (route.view === "settings") return <SettingsView console={console} />;
  const CoreView = coreViewMap[route.view as keyof typeof coreViewMap];
  if (CoreView) return <CoreView console={console} />;
  const View = tabViewMap[route.view]?.[route.tab] ?? tabViewMap[route.view]?.[groupedTabs[route.view as keyof typeof groupedTabs]?.[0]?.id ?? ""];
  if (!View) return null;
  const tabs = groupedTabs[route.view as keyof typeof groupedTabs];
  return <section className="groupedPage"><div className="groupedHeader"><div><p className="eyebrow">Project workspace</p><h1>{groupedTitle(route.view)}</h1><p>{groupedDescription(route.view)}</p></div></div><nav aria-label={`${groupedTitle(route.view)} sections`} className="tabs">{tabs.map((tab) => <a aria-current={route.tab === tab.id ? "page" : undefined} className={route.tab === tab.id ? "active" : ""} href={routeHref({ projectID: route.projectID, view: route.view, tab: tab.id })} key={tab.id} onClick={(event) => { if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); console.navigate({ projectID: route.projectID, view: route.view, tab: tab.id }); }}>{tab.label}</a>)}</nav><View console={console} /></section>;
}

function groupedTitle(view: string) { return view[0].toUpperCase() + view.slice(1); }
function groupedDescription(view: string) {
  return { delivery: "Commit, artifact, rollout, and exposure evidence.", infrastructure: "Runtime placement and Agent-backed infrastructure facts.", observability: "Health, telemetry, logs, incidents, and support evidence.", security: "Protected secret flows and audit history." }[view as keyof typeof groupedTabs] ?? "";
}
