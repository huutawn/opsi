import { Icon } from "@/components/ui/primitives";
import type { DeploymentRun, DeploymentRunEvent, DeploymentRunState } from "@/lib/contracts/registry";

const phases: Array<{ state: DeploymentRunState; label: string; icon: string }> = [
  { state: "building", label: "Build", icon: "layers" },
  { state: "preflighting", label: "Preflight", icon: "shield" },
  { state: "deploying", label: "Deploy", icon: "rocket_launch" },
  { state: "verifying", label: "Verify", icon: "check_circle" },
];

const order: DeploymentRunState[] = ["analyzing", "awaiting_input", "awaiting_approval", "provisioning", "building", "preflighting", "awaiting_warning_ack", "deploying", "verifying", "succeeded"];

export function DeploymentTimeline({ events, run }: { events: DeploymentRunEvent[]; run: DeploymentRun }) {
  const current = order.indexOf(run.state);
  return (
    <section aria-labelledby="run-timeline-title" className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-on-surface" id="run-timeline-title">Deployment run</h2>
        <p className="mt-1 text-sm text-on-surface-variant" aria-live="polite">{stateSummary(run)}</p>
      </div>
      <ol className="grid grid-cols-2 gap-2 sm:grid-cols-4" aria-label="Deployment phases">
        {phases.map((phase) => {
          const phaseIndex = order.indexOf(phase.state);
          const finished = run.state === "succeeded" || current > phaseIndex;
          const active = run.state === phase.state || (phase.state === "preflighting" && run.state === "awaiting_warning_ack");
          return (
            <li className={`border px-3 py-3 ${active ? "border-primary bg-primary-container" : finished ? "border-status-ready/40 bg-state-live-bg" : "border-outline-variant/30 bg-surface-container-low"}`} key={phase.state}>
              <span className="flex items-center gap-2 text-sm font-medium"><Icon className={finished ? "text-status-ready" : active ? "text-primary" : "text-on-surface-variant"} name={finished ? "check" : phase.icon} />{phase.label}</span>
            </li>
          );
        })}
      </ol>
      <ol className="border-l border-outline-variant/50 pl-5" aria-label="Run event timeline">
        {events.length === 0 ? <li className="py-3 text-sm text-on-surface-variant">Waiting for the first factual event…</li> : events.map((event) => (
          <li className="relative pb-5 last:pb-0" key={event.id}>
            <span className={`absolute -left-[1.55rem] top-1.5 h-2 w-2 rounded-full ${event.level === "error" ? "bg-status-failed" : "bg-secondary"}`} />
            <p className="text-sm text-on-surface">{event.message}</p>
            <p className="mt-1 text-xs text-on-surface-variant"><time dateTime={event.created_at}>{new Date(event.created_at).toLocaleString()}</time> · {event.state.replaceAll("_", " ")}</p>
          </li>
        ))}
      </ol>
    </section>
  );
}

function stateSummary(run: DeploymentRun) {
  if (run.state === "awaiting_warning_ack") return "Preflight found warnings. Review and acknowledge this exact result before deployment continues.";
  if (run.state === "failed") return `${run.failure?.step || "A step"} failed: ${run.failure?.message || "No failure detail was returned."}`;
  if (run.state === "succeeded") return "Build, rollout, and verification evidence are available.";
  return `Opsi is ${run.state.replaceAll("_", " ")}. This run continues after refresh or restart.`;
}
