import { StatePanel } from "@/components/ui/primitives";
import { NodesView } from "@/features/console/nodes-view";
import { OperationsViewMap } from "@/features/console/router-map";
import type { ConsoleController } from "@/features/console/types";

export function ConsoleRouter({ console }: { console: ConsoleController }) {
  if (console.state.status === "loading") return <StatePanel title="Loading project state" text="Reading local backend state." />;
  if (console.state.status === "permission")
    return (
      <StatePanel
        title="Sign in required"
        text={console.state.message || "Use Sign in with GitHub in the top bar, then retry."}
        retry={() => void console.actions.load()}
      />
    );
  if (console.state.status === "network")
    return <StatePanel title="Network error" text={console.state.message || "Local backend is unreachable."} retry={() => void console.actions.load()} />;
  if (console.state.status === "error")
    return <StatePanel title="Request failed" text={console.state.message || "The API returned a non-retryable error."} retry={() => void console.actions.load()} />;

  if (console.active === "Servers / Nodes") return <NodesView console={console} />;
  const View = OperationsViewMap[console.active] ?? OperationsViewMap.Projects;
  return <View console={console} />;
}
