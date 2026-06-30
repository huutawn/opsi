import { Empty, Panel } from "@/components/ui/primitives";

export function StaticPanel({ title, text }: { title: string; text: string }) {
  return (
    <Panel title={title}>
      <Empty text={text} />
    </Panel>
  );
}
