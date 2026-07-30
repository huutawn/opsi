"use client";

import { useEffect, useMemo, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
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

  useEffect(() => { queueMicrotask(load); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  function rotate() {
    console.reviewMutation(
      { project: "Local workspace", targetType: "Cloud PAT", targetID: console.session?.project_id || "workspace session", operation: "rotate", diff: ["replace the PAT in the OS secure store", "revoke the previous PAT after replacement"], risk: "Other local processes using the old PAT will lose access." },
      async (key) => {
        const result = await client.rotatePAT(console.state.project?.id, key);
        await console.actions.load();
        return `Local API receipt: rotated ${String(result.rotated)}; previous token revoked ${String(result.revoked_old)}.`;
      },
    );
  }

  function revoke() {
    console.reviewMutation(
      { project: "Local workspace", targetType: "Cloud PAT", targetID: console.session?.project_id || "workspace session", operation: "revoke and sign out", diff: ["revoke Cloud access", "delete the PAT from the OS secure store", "invalidate the Local browser session"], risk: "Destructive: the console returns to signed-out state.", confirmation: "REVOKE" },
      async (key) => {
        const result = await client.revokePAT(console.state.project?.id, key);
        console.actions.hideSensitive();
        await console.actions.load();
        return `Local API receipt: PAT revoked ${String(result.revoked)}; authenticated ${String(result.authenticated)}.`;
      },
    );
  }

  if (error) return <div className="errorBox" role="alert"><b>Settings unavailable</b><span>{error}</span><button onClick={load} type="button">Retry</button></div>;
  if (!settings) return <p aria-live="polite" role="status">Loading Local backend settings…</p>;

  return <main className="settingsPage">
    <header className="settingsHeader"><div><p className="eyebrow">Local workspace</p><h1>Settings</h1><p>Session, connection, installation, and access-token facts for this Opsi install.</p></div>{console.state.project ? <button onClick={() => console.navigate({ view: "overview" })} type="button">Return to {console.state.project.name}</button> : null}</header>
    <section className="settingsSection" aria-labelledby="settings-session"><div className="sectionHeading"><div><h2 id="settings-session">Session</h2><p>Verified through the Local session boundary.</p></div><StatusBadge value={console.session?.authenticated ? "healthy" : "unavailable"} /></div><dl className="settingsFacts"><Fact label="State" value={console.session?.authenticated ? "Authenticated" : "Signed out"} /><Fact label="Organization" value={console.session?.org_id || "Not reported"} /><Fact label="Selected project context" value={console.state.project ? `${console.state.project.name} (${console.state.project.id})` : "None"} /><Fact label="Token status" value={console.session?.token_status || "Unknown"} /></dl></section>
    <section className="settingsSection" aria-labelledby="settings-connections"><div className="sectionHeading"><div><h2 id="settings-connections">Connections</h2><p>Connection and trust configuration without credential material.</p></div></div><dl className="settingsFacts"><Fact label="Cloud connection" value={connectionLabel(console.session?.cloud_connected)} status={connectionStatus(console.session?.cloud_connected)} /><Fact label="Agent connection" value={connectionLabel(console.session?.agent_connected)} status={connectionStatus(console.session?.agent_connected)} /><Fact label="Cloud authority" value={settings.cloud_authority || "Missing"} /><Fact label="Agent TLS pin" value={settings.agent_tls_pinned ? "Configured" : "Missing"} status={settings.agent_tls_pinned ? "healthy" : "unknown"} /></dl></section>
    <section className="settingsSection" aria-labelledby="settings-version"><div className="sectionHeading"><div><h2 id="settings-version">Version and installation</h2><p>Build and installed-asset facts reported by the Local backend.</p></div></div><dl className="settingsFacts"><Fact label="CLI/UI version" value={settings.version || "Missing"} /><Fact label="Revision" value={settings.revision || "Missing"} mono /><Fact label="Go version" value={settings.go_version || "Missing"} /><Fact label="Installed UI assets" value={settings.ui_assets || "Missing"} /><Fact label="Configuration selected" value={settings.config_selected ? "Configured" : "Missing"} status={settings.config_selected ? "healthy" : "unknown"} /></dl></section>
    <section className="settingsSection" aria-labelledby="settings-token"><div className="sectionHeading"><div><h2 id="settings-token">Access token lifecycle</h2><p>Status is verified by the existing Local session request. Token material is never shown.</p></div></div><div className="tokenActions"><button className="primary" disabled={!console.session?.authenticated} onClick={rotate} type="button">Review PAT rotation</button><button disabled={!console.session?.authenticated} onClick={revoke} type="button">Review revoke and sign out</button><button onClick={() => void console.actions.load()} type="button">Refresh status</button></div></section>
    <details className="capabilityLimits"><summary>Capability limits</summary>{settings.backend_gaps.length ? <dl>{settings.backend_gaps.map((gap) => <div key={gap.capability}><dt>{gap.capability}</dt><dd>{gap.status}. Roadmap: {gap.roadmap}.</dd></div>)}</dl> : <Empty text="No backend capability limits were reported." />}</details>
  </main>;
}

function Fact({ label, value, status, mono = false }: { label: string; value: string; status?: string; mono?: boolean }) { return <div><dt>{label}</dt><dd>{status ? <StatusBadge label={value} value={status} /> : mono ? <code>{value}</code> : value}</dd></div>; }
function connectionLabel(value?: string) { return value === "ok" ? "Connected" : value === "failed" ? "Unavailable" : "Unknown"; }
function connectionStatus(value?: string) { return value === "ok" ? "healthy" : value === "failed" ? "unavailable" : "unknown"; }
