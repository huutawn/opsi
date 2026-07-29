import { StatePanel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { routeView } from "@/features/console/router-map";

export function ConsoleRouter({ console }: { console: ConsoleController }) {
  if (console.state.status === "loading") return <StatePanel title="Loading project state" text="Reading factual data through the Local API…" />;
  if (console.state.status === "permission") return <StatePanel title="Sign in required" text={console.state.message || "Use the local sign-in flow to continue."} />;
  if (console.state.status === "network") return <StatePanel title="Local data unavailable" text={console.state.message || "The Local API is unreachable."} retry={() => void console.actions.load()} />;
  if (console.state.status === "error") return <StatePanel title="Request failed" text={console.state.message || "The Local API returned a non-retryable error."} retry={() => void console.actions.load()} />;
  return routeView(console.route, console);
}
