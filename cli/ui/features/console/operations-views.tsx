import { AuditView } from "@/features/audit/audit-view";
import { StaticPanel } from "@/features/console/shared";
import { DeploymentsView } from "@/features/deployments/deployments-view";
import { TopologyView } from "@/features/topology/topology-view";

export function PlaceholderView({ title, text }: { title: string; text: string }) {
  return <StaticPanel title={title} text={text} />;
}

export { AuditView, DeploymentsView, TopologyView };
