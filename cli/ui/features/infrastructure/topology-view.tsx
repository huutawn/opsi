"use client";

import { useEffect, useRef, useState } from "react";
import { Empty } from "@/components/ui/primitives";
import { ApplicationWizard } from "@/features/applications/application-wizard";
import type { ConsoleController } from "@/features/console/types";
import { PlacementDialog } from "@/features/infrastructure/placement-dialog";
import { useInfrastructureData } from "@/features/infrastructure/data";
import { currentEnvironment } from "@/lib/presentation/infrastructure/model";
import { BootstrapDialog, TopologyTab } from "./infrastructure-view";

export function TopologyView({ console }: { console: ConsoleController }) {
  const { data, source, error, load } = useInfrastructureData(console);
  const mode = console.route.topologyMode === "live" ? "live" : "design";
  const [placementOpen, setPlacementOpen] = useState(false);
  const [bootstrapOpen, setBootstrapOpen] = useState(false);
  const [serviceOpen, setServiceOpen] = useState(false);
  const placementTrigger = useRef<HTMLButtonElement>(null);
  const bootstrapTrigger = useRef<HTMLButtonElement>(null);
  const serviceTrigger = useRef<HTMLButtonElement>(null);
  const projectID = console.state.project?.id ?? "";

  useEffect(() => {
    // Project-scoped dialogs never survive a context change.
    queueMicrotask(() => {
      setPlacementOpen(false);
      setBootstrapOpen(false);
      setServiceOpen(false);
    });
  }, [projectID]);

  if (!console.state.project) return <Empty text="Select a project first." />;
  if (source === "loading" && !data.facts) {
    return <Empty title="Loading topology…" text="Reading factual topology and runtime inventory from Local API." />;
  }
  if (!data.facts) {
    return (
      <Empty
        action={
          <button onClick={() => void load()} type="button">
            Retry
          </button>
        }
        text={error || "Local API did not return topology facts."}
        title="Topology unavailable"
      />
    );
  }
  const environment = currentEnvironment(data.facts, console.route.environment ?? "");

  return (
    <div className="infrastructurePage topologyPage">
      <TopologyTab
        bindings={data.bindings}
        builds={data.builds}
        console={console}
        environment={environment}
        error={error}
        facts={data.facts}
        key={`${projectID}:${environment?.id ?? "unresolved"}`}
        mode={mode}
        onAddService={(trigger) => {
          serviceTrigger.current = trigger;
          setServiceOpen(true);
        }}
        onConnectServer={(trigger) => {
          bootstrapTrigger.current = trigger;
          setBootstrapOpen(true);
        }}
        onMode={(next) => console.navigate({ topologyMode: next })}
        onPlanPlacement={(trigger) => {
          placementTrigger.current = trigger;
          setPlacementOpen(true);
        }}
        onReload={load}
        policies={data.policies}
        repositories={data.repositories}
        topology={data.topology}
      />
      {placementOpen ? (
        <PlacementDialog
          console={console}
          data={{
            facts: data.facts,
            topology: data.topology,
            repositories: data.repositories,
            bindings: data.bindings,
            builds: data.builds,
            policies: data.policies,
          }}
          onApplied={() => {
            void console.actions.load();
            void load();
          }}
          onClose={() => {
            setPlacementOpen(false);
            window.requestAnimationFrame(() => placementTrigger.current?.focus());
          }}
        />
      ) : null}
      {bootstrapOpen ? (
        <BootstrapDialog
          console={console}
          onClose={() => {
            setBootstrapOpen(false);
            window.requestAnimationFrame(() => bootstrapTrigger.current?.focus());
          }}
          onCreated={load}
        />
      ) : null}
      {serviceOpen ? (
        <ApplicationWizard
          console={console}
          onClose={() => {
            setServiceOpen(false);
            window.requestAnimationFrame(() => serviceTrigger.current?.focus());
          }}
          onCreated={async () => {
            console.navigate({ topologyMode: "design" });
            await load();
          }}
        />
      ) : null}
    </div>
  );
}
