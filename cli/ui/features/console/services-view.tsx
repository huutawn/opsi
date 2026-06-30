import { Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { AddServiceForm } from "@/features/services/add-service-form";
import { ServiceDetail } from "@/features/services/service-detail";
import { ServicesList } from "@/features/services/services-list";

export function ServicesView({ console }: { console: ConsoleController }) {
  return (
    <section className="grid">
      <AddServiceForm console={console} />
      <Panel title="Services">
        <ServicesList console={console} />
      </Panel>
      <ServiceDetail console={console} />
    </section>
  );
}
