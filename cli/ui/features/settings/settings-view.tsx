"use client";

import { useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient, type LocalSettings } from "@/lib/api/local-client";

export function SettingsView({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const [settings, setSettings] = useState<LocalSettings | null>(null);
  const [error, setError] = useState("");

  function load() {
    setError("");
    client.settings().then(setSettings).catch((cause: Error) => setError(cause.message));
  }

  useEffect(() => {
    queueMicrotask(() => load());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function rotate() {
    console.reviewMutation(
      { project: console.state.project?.name || "current session", targetType: "Cloud PAT", targetID: console.session?.project_id || "session", operation: "rotate", diff: ["replace the OS-keychain PAT", "revoke the previous PAT after replacement"], risk: "Other processes using the old PAT will lose access." },
      async (key) => {
        const result = await client.rotatePAT(console.state.project?.id, key);
        await console.actions.load();
        return `PAT rotated; previous token revoked: ${String(result.revoked_old)}.`;
      },
    );
  }

  function revoke() {
    const target = console.session?.project_id || "session";
    console.reviewMutation(
      { project: console.state.project?.name || "current session", targetType: "Cloud PAT", targetID: target, operation: "revoke", diff: ["revoke Cloud access", "delete the PAT from the OS keychain"], risk: "Destructive: the browser returns to sign-in.", confirmation: target },
      async (key) => {
        const result = await client.revokePAT(console.state.project?.id, key);
        await console.actions.load();
        return `PAT revoked: ${String(result.revoked)}.`;
      },
    );
  }

  if (error) return <div className="errorBox" role="alert"><b>{error}</b><button onClick={load} type="button">Retry</button></div>;
  if (!settings) return <p aria-live="polite" role="status">Loading Local backend settings...</p>;
  return (
    <section className="grid cols">
      <Panel title="Version and configuration">
        <dl className="reviewFacts">
          <div><dt>CLI/UI version</dt><dd>{settings.version}</dd></div>
          <div><dt>Revision</dt><dd><code>{settings.revision}</code></dd></div>
          <div><dt>Go</dt><dd>{settings.go_version}</dd></div>
          <div><dt>Cloud authority</dt><dd>{settings.cloud_authority || "not configured"}</dd></div>
          <div><dt>Agent TLS pin</dt><dd><StatusBadge value={settings.agent_tls_pinned ? "configured" : "missing"} /></dd></div>
          <div><dt>Installed UI assets</dt><dd>{settings.ui_assets}</dd></div>
        </dl>
      </Panel>
      <Panel title="Session and PAT">
        <p>Role: <b>{console.session?.authenticated ? "authenticated" : "signed out"}</b></p>
        <div className="buttonRow"><button onClick={rotate} type="button">Review PAT rotation</button><button onClick={revoke} type="button">Review PAT revoke</button></div>
      </Panel>
      <Panel title="Backend gaps">
        {settings.backend_gaps.length ? settings.backend_gaps.map((gap) => (
          <div className="unavailable" key={gap.capability}>
            <div><b>{gap.capability}</b><p>No factual API exists. Roadmap: {gap.roadmap}.</p></div>
            <button disabled title="Unavailable because the backend capability does not exist" type="button">{gap.status}</button>
          </div>
        )) : <Empty text="No recorded backend gaps." />}
      </Panel>
    </section>
  );
}
