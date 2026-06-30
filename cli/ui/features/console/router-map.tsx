import { AuditView, DeploymentsView, PlaceholderView, TopologyView } from "@/features/console/operations-views";
import { OverviewView } from "@/features/console/overview-view";
import { ProjectsView } from "@/features/console/projects-view";
import { ServicesView } from "@/features/console/services-view";
import type { ConsoleController } from "@/features/console/types";
import { IncidentsView } from "@/features/incidents/incidents-view";
import { SecretsView } from "@/features/secrets/secrets-view";

export const OperationsViewMap: Record<string, (props: { console: ConsoleController }) => React.ReactNode> = {
  Projects: ProjectsView,
  Overview: OverviewView,
  Services: ServicesView,
  Deployments: DeploymentsView,
  Topology: TopologyView,
  Audit: AuditView,
  Secrets: SecretsView,
  Logs: () => <PlaceholderView title="Logs" text="Logs stream from Agent storage after a workload is deployed." />,
  Metrics: () => <PlaceholderView title="Metrics" text="Metrics stream from Agent telemetry after node inventory is healthy." />,
  "Incidents & RCA": IncidentsView,
  Settings: () => <PlaceholderView title="Settings" text="Cloud URL and PAT are held in memory. PAT is not persisted by this UI." />,
};
