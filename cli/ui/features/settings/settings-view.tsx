"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Empty, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient, type LocalSettings } from "@/lib/api/local-client";
import { groupedTabs, routeHref } from "@/features/console/navigation";
import { Tabs, tabPanelProps } from "@/components/navigation/tabs";

export function SettingsView({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const [settings, setSettings] = useState<LocalSettings | null>(null);
  const [error, setError] = useState("");

  function load() {
    setError("");
    client.settings().then(setSettings).catch((cause: Error) => setError(cause.message));
  }

  useEffect(() => {
    queueMicrotask(load);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  function rotate() {
    console.reviewMutation(
      {
        project: "Local workspace",
        targetType: "Cloud PAT",
        targetID: console.session?.project_id || "workspace session",
        operation: "PAT rotation",
        diff: ["replace the PAT in the OS secure store", "revoke the previous PAT after replacement"],
        risk: "Other local processes using the old PAT will lose access.",
      },
      async (key) => {
        const result = await client.rotatePAT(console.state.project?.id, key);
        await console.actions.load();
        return `Local API receipt: rotated ${String(result.rotated)}; previous token revoked ${String(result.revoked_old)}.`;
      }
    );
  }

  function revoke() {
    console.reviewMutation(
      {
        project: "Local workspace",
        targetType: "Cloud PAT",
        targetID: console.session?.project_id || "workspace session",
        operation: "revoke and sign out",
        diff: ["revoke Cloud access", "delete the PAT from the OS secure store", "invalidate the Local browser session"],
        risk: "Destructive: the console returns to signed-out state.",
        confirmation: "REVOKE",
      },
      async (key) => {
        const result = await client.revokePAT(console.state.project?.id, key);
        console.actions.hideSensitive();
        await console.actions.load();
        return `Local API receipt: PAT revoked ${String(result.revoked)}; authenticated ${String(result.authenticated)}.`;
      }
    );
  }

  if (error) {
    return (
      <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
        <div className="bg-error-container/20 border border-error/30 p-6 rounded-xl text-error text-xs flex items-center justify-between gap-4" role="alert">
          <div className="flex items-center gap-3">
            <Icon name="error" className="text-[22px] shrink-0" />
            <div>
              <strong className="block text-sm">Settings Unavailable</strong>
              <span>{error}</span>
            </div>
          </div>
          <Button onClick={load} size="sm" variant="outline">
            Retry
          </Button>
        </div>
      </div>
    );
  }

  if (!settings) {
    return (
      <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
        <div className="flex items-center gap-3 text-on-surface-variant text-xs">
          <Icon name="sync" className="animate-spin text-[18px]" />
          <span>Loading Local Edge backend settings…</span>
        </div>
      </div>
    );
  }

  const tab = console.route.tab || "general";

  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      <PageHeader
        action={
          console.state.project ? (
            <Button onClick={() => console.navigate({ view: "overview" })} variant="secondary">
              Return to {console.state.project.name}
            </Button>
          ) : null
        }
        description="Session, authority connections, software version, and access token settings."
        eyebrow="Local Workspace"
        icon="settings"
        title="Settings"
      />

      <Tabs
        items={groupedTabs.settings.map((item) => ({ ...item, href: routeHref({ ...console.route, tab: item.id }) }))}
        label="Settings sections"
        onSelect={(event, next) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          event.preventDefault();
          console.navigate({ tab: next });
        }}
        selected={tab}
      />

      <div {...tabPanelProps("Settings sections", tab)}>
        {tab === "general" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-4">
              <div>
                <h3 className="font-headline-md text-lg font-bold text-on-surface">General</h3>
                <p className="text-xs text-on-surface-variant mt-0.5">Current Local workspace and active project context.</p>
              </div>
              <StatusBadge value={console.session?.authenticated ? "healthy" : "unavailable"} />
            </div>

            <dl className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
              <Fact label="State" value={console.session?.authenticated ? "Authenticated" : "Signed Out"} />
              <Fact label="Organization" value={console.session?.org_id || "Not reported"} />
              <Fact label="Selected project context" value={console.state.project ? `${console.state.project.name} (${console.state.project.id})` : "None"} />
              <Fact label="Token status" value={console.session?.token_status || "Active in OS store"} />
            </dl>
          </div>
        ) : null}

        {tab === "authentication" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="border-b border-outline-variant/20 pb-4">
              <h3 className="font-headline-md text-lg font-bold text-on-surface">Authentication</h3>
              <p className="text-xs text-on-surface-variant mt-0.5">
                PAT lifecycle uses the existing Local session boundary; token material is stored in the OS keychain and never exposed in the browser.
              </p>
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button disabled={!console.session?.authenticated} onClick={rotate} variant="primary">
                <Icon name="rotate_right" className="text-[18px]" />
                Review PAT rotation
              </Button>
              <Button disabled={!console.session?.authenticated} onClick={revoke} variant="danger">
                <Icon name="logout" className="text-[18px]" />
                Review revoke and sign out
              </Button>
              <Button onClick={() => void console.actions.load()} variant="secondary">
                <Icon name="refresh" className="text-[18px]" />
                Refresh status
              </Button>
            </div>
          </div>
        ) : null}

        {tab === "integrations" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="border-b border-outline-variant/20 pb-4">
              <h3 className="font-headline-md text-lg font-bold text-on-surface">Integrations</h3>
              <p className="text-xs text-on-surface-variant mt-0.5">Connection state and trust boundaries without sensitive token leakage.</p>
            </div>

            <dl className="settingsFacts grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
              <Fact label="Cloud connection" status={connectionStatus(console.session?.cloud_connected)} value={connectionLabel(console.session?.cloud_connected)} />
              <Fact label="Agent connection" status={connectionStatus(console.session?.agent_connected)} value={connectionLabel(console.session?.agent_connected)} />
              <Fact label="Cloud authority" value={settings.cloud_authority || "Missing"} />
              <Fact label="Agent TLS pin" status={settings.agent_tls_pinned ? "healthy" : "unknown"} value={settings.agent_tls_pinned ? "Configured" : "Missing"} />
            </dl>
          </div>
        ) : null}

        {tab === "system" ? (
          <div className="space-y-6">
            <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
              <div className="border-b border-outline-variant/20 pb-4">
                <h3 className="font-headline-md text-lg font-bold text-on-surface">System</h3>
                <p className="text-xs text-on-surface-variant mt-0.5">Build and installed asset facts reported by the Local binary.</p>
              </div>

              <dl className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
                <Fact label="CLI/UI version" value={settings.version || "Missing"} />
                <Fact label="Revision" mono value={settings.revision || "Missing"} />
                <Fact label="Go version" value={settings.go_version || "Missing"} />
                <Fact label="Installed UI assets" value={settings.ui_assets || "Missing"} />
              </dl>
            </div>

            <details className="capabilityLimits bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
              <summary className="font-headline-md text-sm font-bold text-on-surface cursor-pointer">Capability limits</summary>
              {settings.backend_gaps.length ? (
                <dl className="space-y-2 mt-4">
                  {settings.backend_gaps.map((gap) => (
                    <div key={gap.capability} className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 text-xs">
                      <dt className="text-on-surface font-semibold inline">
                        <span>{gap.capability}</span>:{" "}
                      </dt>
                      <dd className="inline text-on-surface-variant">{gap.status}. Roadmap: {gap.roadmap}.</dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <Empty text="No backend capability limits were reported." />
              )}
            </details>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function Fact({ label, value, status, mono = false }: { label: string; value: string; status?: string; mono?: boolean }) {
  return (
    <div className="flex flex-col min-w-0">
      <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider">{label}</dt>
      <dd className="mt-1">
        {status ? (
          <StatusBadge label={value} value={status} />
        ) : (
          <span className={`text-xs text-on-surface font-semibold truncate ${mono ? "font-code-md" : "font-body-md"}`}>{value}</span>
        )}
      </dd>
    </div>
  );
}

function connectionLabel(value?: string) {
  return value === "ok" ? "Connected" : value === "failed" ? "Unavailable" : value === "not connected" ? "Not connected" : "Unknown";
}

function connectionStatus(value?: string) {
  return value === "ok" ? "healthy" : value === "failed" ? "unavailable" : "unknown";
}
