import { AuditView, DeploymentsView, TopologyView } from "@/features/console/operations-views";
import { OverviewView } from "@/features/console/overview-view";
import { ProjectsView } from "@/features/console/projects-view";
import { ServicesView } from "@/features/console/services-view";
import type { ConsoleController } from "@/features/console/types";
import { IncidentsView } from "@/features/incidents/incidents-view";
import { LogsView } from "@/features/logs/logs-view";
import { SecretsView } from "@/features/secrets/secrets-view";
import { SupportView } from "@/features/support/support-view";
import { GitHubView } from "@/features/github/github-view";
import { BuildRecordsView } from "@/features/build-records/build-records-view";
import { MetricsView } from "@/features/metrics/metrics-view";
import { RuntimeView } from "@/features/runtime/runtime-view";
import { SettingsView } from "@/features/settings/settings-view";

export const OperationsViewMap: Record<string, (props: { console: ConsoleController }) => React.ReactNode> = {
  Projects: ProjectsView,
  GitHub: GitHubView,
  Overview: OverviewView,
  Runtime: RuntimeView,
  Services: ServicesView,
  "Build Records": BuildRecordsView,
  Deployments: DeploymentsView,
  Topology: TopologyView,
  Audit: AuditView,
  Secrets: SecretsView,
  Logs: LogsView,
  Metrics: MetricsView,
  Support: SupportView,
  Incidents: IncidentsView,
  Settings: SettingsView,
};
