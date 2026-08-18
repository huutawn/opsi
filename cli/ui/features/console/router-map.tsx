import { DeliveryView } from "@/features/delivery/delivery-view";
import { InfrastructureView } from "@/features/infrastructure/infrastructure-view";
import { InfrastructureCenterView } from "@/features/infrastructure/infrastructure-center";
import { TopologyView } from "@/features/infrastructure/topology-view";
import { ObservabilityView } from "@/features/observability/observability-view";
import { SecurityView } from "@/features/security/security-view";
import { SettingsView } from "@/features/settings/settings-view";
import { groupedTabs, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import { OverviewView } from "@/features/overview/overview-view";
import { ProjectsView, WorkspaceHomeView } from "@/features/projects/projects-view";
import { Tabs, tabPanelProps } from "@/components/navigation/tabs";
import { ServicesView } from "@/features/services/services-view";
import { Button, Icon } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export const coreViewMap = {
  overview: OverviewView,
  services: ServicesView,
  topology: TopologyView,
} as const;

const tabViewMap: Record<string, Record<string, (props: { console: ConsoleController }) => React.ReactNode>> = {
  delivery: { pipeline: DeliveryView, builds: DeliveryView, deployments: DeliveryView, exposure: DeliveryView, source: DeliveryView },
  infrastructure: {
    servers: InfrastructureCenterView,
    resources: InfrastructureCenterView,
    storage: InfrastructureCenterView,
    // backward-compatibility mappings
    topology: TopologyView,
    runtimes: InfrastructureView,
    nodes: InfrastructureView,
    bootstrap: InfrastructureView,
  },
  observability: {
    overview: ObservabilityView,
    applications: ObservabilityView,
    servers: ObservabilityView,
    resources: ObservabilityView,
    // backward-compatibility mappings
    health: ObservabilityView,
    metrics: ObservabilityView,
    logs: ObservabilityView,
    incidents: ObservabilityView,
  },
  security: { overview: SecurityView, audit: SecurityView, access: SecurityView, secrets: SecurityView },
};

const legacyTabGroups: Record<string, Record<string, ReadonlyArray<{ id: string; label: string }>>> = {
  infrastructure: {
    topology: [
      { id: "topology", label: "Topology" },
      { id: "runtimes", label: "Runtimes" },
      { id: "nodes", label: "Nodes" },
      { id: "bootstrap", label: "Bootstrap" },
    ],
    runtimes: [
      { id: "topology", label: "Topology" },
      { id: "runtimes", label: "Runtimes" },
      { id: "nodes", label: "Nodes" },
      { id: "bootstrap", label: "Bootstrap" },
    ],
    nodes: [
      { id: "topology", label: "Topology" },
      { id: "runtimes", label: "Runtimes" },
      { id: "nodes", label: "Nodes" },
      { id: "bootstrap", label: "Bootstrap" },
    ],
    bootstrap: [
      { id: "topology", label: "Topology" },
      { id: "runtimes", label: "Runtimes" },
      { id: "nodes", label: "Nodes" },
      { id: "bootstrap", label: "Bootstrap" },
    ],
  },
  observability: {
    health: [
      { id: "health", label: "Health" },
      { id: "metrics", label: "Metrics" },
      { id: "logs", label: "Logs" },
      { id: "incidents", label: "Incidents" },
    ],
    metrics: [
      { id: "health", label: "Health" },
      { id: "metrics", label: "Metrics" },
      { id: "logs", label: "Logs" },
      { id: "incidents", label: "Incidents" },
    ],
    logs: [
      { id: "health", label: "Health" },
      { id: "metrics", label: "Metrics" },
      { id: "logs", label: "Logs" },
      { id: "incidents", label: "Incidents" },
    ],
    incidents: [
      { id: "health", label: "Health" },
      { id: "metrics", label: "Metrics" },
      { id: "logs", label: "Logs" },
      { id: "incidents", label: "Incidents" },
    ],
  },
  security: {
    secrets: [
      { id: "overview", label: "Overview" },
      { id: "audit", label: "Audit" },
      { id: "access", label: "Access & Identities" },
    ],
  },
};

export function routeView(route: ConsoleRoute, console: ConsoleController) {
  if (route.view === "projects") return <ProjectsView console={console} />;
  if (route.view === "home") return <WorkspaceHomeView console={console} />;
  if (route.view === "settings") return <SettingsView console={console} />;
  const CoreView = coreViewMap[route.view as keyof typeof coreViewMap];
  if (CoreView) return <CoreView console={console} />;
  const View = (route.view === "security" ? SecurityView : undefined) ?? tabViewMap[route.view]?.[route.tab] ?? tabViewMap[route.view]?.[groupedTabs[route.view as keyof typeof groupedTabs]?.[0]?.id ?? ""];
  if (!View) return null;
  const tabs = legacyTabGroups[route.view]?.[route.tab] ?? groupedTabs[route.view as keyof typeof groupedTabs];
  const label = `${groupedTitle(route.view)} sections`;
  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div>
          <h1 className="font-headline-lg text-headline-lg font-semibold text-on-surface">
            {groupedTitle(route.view)}
          </h1>
          <p className="font-body-md text-sm text-on-surface-variant mt-1">
            {groupedDescription(route.view)}
          </p>
        </div>
        {route.view === "delivery" && (
          <Button
            onClick={() => console.navigate({ view: "services" })}
            size="md"
            variant="primary"
          >
            <Icon name="rocket_launch" className="text-[18px]" />
            New Deployment
          </Button>
        )}
      </div>
      <Tabs
        items={tabs.map((tab) => ({ ...tab, href: routeHref({ ...route, tab: tab.id }) }))}
        label={label}
        onSelect={(event, tabId) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          event.preventDefault();
          console.navigate({ ...route, tab: tabId });
        }}
        selected={route.tab}
      />
      <div {...tabPanelProps(label, route.tab)}>
        <View console={console} />
      </div>
    </div>
  );
}

function groupedTitle(view: string) {
  return view[0].toUpperCase() + view.slice(1);
}

function groupedDescription(view: string) {
  const descriptions: Record<string, string> = {
    delivery: "Commit, artifact, rollout, and exposure evidence.",
    infrastructure: "Factual server execution capacity, managed resources, and persistent database storage.",
    observability: "Factual runtime health, application diagnostics, server telemetry, and managed resource readiness.",
    security: "Security visibility, authenticated identity boundaries, safe credential status, and audit history.",
  };
  return descriptions[view] ?? "";
}
