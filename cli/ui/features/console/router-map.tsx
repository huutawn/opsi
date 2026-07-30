import { DeliveryView } from "@/features/delivery/delivery-view";
import { InfrastructureView } from "@/features/infrastructure/infrastructure-view";
import { ObservabilityView } from "@/features/observability/observability-view";
import { SecurityView } from "@/features/security/security-view";
import { SettingsView } from "@/features/settings/settings-view";
import { groupedTabs, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import { OverviewView } from "@/features/overview/overview-view";
import { ProjectsView } from "@/features/projects/projects-view";
import { ServicesView } from "@/features/services/services-view";
import type { ConsoleController } from "@/features/console/types";

export const coreViewMap = { overview: OverviewView, services: ServicesView } as const;

const tabViewMap: Record<string, Record<string, (props: { console: ConsoleController }) => React.ReactNode>> = {
  delivery: { pipeline: DeliveryView, builds: DeliveryView, deployments: DeliveryView, exposure: DeliveryView, source: DeliveryView },
  infrastructure: { topology: InfrastructureView, runtimes: InfrastructureView, nodes: InfrastructureView, bootstrap: InfrastructureView },
  observability: { health: ObservabilityView, metrics: ObservabilityView, logs: ObservabilityView, incidents: ObservabilityView },
  security: { secrets: SecurityView, audit: SecurityView },
};

export function routeView(route: ConsoleRoute, console: ConsoleController) {
  if (route.view === "projects") return <ProjectsView console={console} />;
  if (route.view === "settings") return <SettingsView console={console} />;
  const CoreView = coreViewMap[route.view as keyof typeof coreViewMap];
  if (CoreView) return <CoreView console={console} />;
  const View = tabViewMap[route.view]?.[route.tab] ?? tabViewMap[route.view]?.[groupedTabs[route.view as keyof typeof groupedTabs]?.[0]?.id ?? ""];
  if (!View) return null;
  const tabs = groupedTabs[route.view as keyof typeof groupedTabs];
  return <section className="groupedPage"><div className="groupedHeader"><div><p className="eyebrow">Project workspace</p><h1>{groupedTitle(route.view)}</h1><p>{groupedDescription(route.view)}</p></div></div><nav aria-label={`${groupedTitle(route.view)} sections`} className="tabs">{tabs.map((tab) => <a aria-current={route.tab === tab.id ? "page" : undefined} className={route.tab === tab.id ? "active" : ""} href={routeHref({ ...route, tab: tab.id })} key={tab.id} onClick={(event) => { if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); console.navigate({ tab: tab.id }); }}>{tab.label}</a>)}</nav><View console={console} /></section>;
}

function groupedTitle(view: string) { return view[0].toUpperCase() + view.slice(1); }
function groupedDescription(view: string) {
  return { delivery: "Commit, artifact, rollout, and exposure evidence.", infrastructure: "Runtime placement and Agent-backed infrastructure facts.", observability: "Health, telemetry, logs, incidents, and support evidence.", security: "Protected secret flows and audit history." }[view as keyof typeof groupedTabs] ?? "";
}
