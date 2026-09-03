"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Empty, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient, type LocalSettings } from "@/lib/api/local-client";
import { groupedTabs, routeHref } from "@/features/console/navigation";
import { Tabs, tabPanelProps } from "@/components/navigation/tabs";
import { useI18n, type Locale } from "@/lib/i18n";

export function SettingsView({ console }: { console: ConsoleController }) {
  const { locale, setLocale, t } = useI18n();
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
              <strong className="block text-sm">{t("settings.error_title", "Settings Unavailable")}</strong>
              <span>{error}</span>
            </div>
          </div>
          <Button onClick={load} size="sm" variant="outline">
            {t("common.retry", "Retry")}
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
          <span>{t("settings.loading", "Loading Local Edge backend settings…")}</span>
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
              {t("settings.return_to_project", { name: console.state.project.name }, `Return to ${console.state.project.name}`)}
            </Button>
          ) : null
        }
        description={t("settings.description", "Session, authority connections, software version, and access token settings.")}
        eyebrow={t("settings.eyebrow", "Local Workspace")}
        icon="settings"
        title={t("settings.title", "Settings")}
      />

      <Tabs
        items={groupedTabs.settings.map((item) => ({
          ...item,
          label: item.id === "general" ? t("settings.tab_general", "General")
            : item.id === "authentication" ? t("settings.tab_authentication", "Authentication")
            : item.id === "integrations" ? t("settings.tab_integrations", "Integrations")
            : t("settings.tab_system", "System"),
          href: routeHref({ ...console.route, tab: item.id }),
        }))}
        label={t("settings.sections_label", "Settings sections")}
        onSelect={(event, next) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          event.preventDefault();
          console.navigate({ tab: next });
        }}
        selected={tab}
      />

      <div {...tabPanelProps(t("settings.sections_label", "Settings sections"), tab)}>
        {tab === "general" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-4">
              <div>
                <h3 className="font-headline-md text-lg font-bold text-on-surface">{t("settings.tab_general", "General")}</h3>
                <p className="text-xs text-on-surface-variant mt-0.5">{t("settings.general_desc", "Current Local workspace and active project context.")}</p>
              </div>
              <StatusBadge value={console.session?.authenticated ? "healthy" : "unavailable"} />
            </div>

            <dl className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
              <Fact label={t("settings.state", "State")} value={console.session?.authenticated ? t("settings.authenticated", "Authenticated") : t("settings.signed_out", "Signed Out")} />
              <Fact label={t("settings.organization", "Organization")} value={console.session?.org_id || t("common.not_reported", "Not reported")} />
              <Fact label={t("settings.selected_project", "Selected project context")} value={console.state.project ? `${console.state.project.name} (${console.state.project.id})` : t("common.none", "None")} />
              <Fact label={t("settings.token_status", "Token status")} value={console.session?.token_status || t("settings.token_active_os", "Active in OS store")} />
            </dl>

            <div className="border-t border-outline-variant/20 pt-6 space-y-3">
              <div>
                <label htmlFor="settings-language-select" className="font-headline-md text-sm font-bold text-on-surface block">
                  {t("settings.language", "Language")}
                </label>
                <p className="text-xs text-on-surface-variant mt-0.5">
                  {t("settings.language_desc", "Local Web UI interface display language.")}
                </p>
              </div>
              <div className="max-w-xs">
                <select
                  id="settings-language-select"
                  aria-label={t("settings.language", "Language")}
                  className="w-full bg-surface-container-high border border-outline-variant/30 rounded-lg px-3 py-2 text-sm text-on-surface font-medium focus:outline-none focus:border-primary/50 cursor-pointer min-h-[40px]"
                  value={locale}
                  onChange={(e) => setLocale(e.target.value as Locale)}
                >
                  <option value="en">{t("settings.language_en", "English")}</option>
                  <option value="vi">{t("settings.language_vi", "Tiếng Việt")}</option>
                </select>
              </div>
            </div>
          </div>
        ) : null}

        {tab === "authentication" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="border-b border-outline-variant/20 pb-4">
              <h3 className="font-headline-md text-lg font-bold text-on-surface">{t("settings.tab_authentication", "Authentication")}</h3>
              <p className="text-xs text-on-surface-variant mt-0.5">
                {t("settings.auth_desc", "PAT lifecycle uses the existing Local session boundary; token material is stored in the OS keychain and never exposed in the browser.")}
              </p>
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button disabled={!console.session?.authenticated} onClick={rotate} variant="primary">
                <Icon name="rotate_right" className="text-[18px]" />
                {t("settings.pat_rotation", "Review PAT rotation")}
              </Button>
              <Button disabled={!console.session?.authenticated} onClick={revoke} variant="danger">
                <Icon name="logout" className="text-[18px]" />
                {t("settings.revoke_sign_out", "Review revoke and sign out")}
              </Button>
              <Button onClick={() => void console.actions.load()} variant="secondary">
                <Icon name="refresh" className="text-[18px]" />
                {t("settings.refresh_status", "Refresh status")}
              </Button>
            </div>
          </div>
        ) : null}

        {tab === "integrations" ? (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
            <div className="border-b border-outline-variant/20 pb-4">
              <h3 className="font-headline-md text-lg font-bold text-on-surface">{t("settings.tab_integrations", "Integrations")}</h3>
              <p className="text-xs text-on-surface-variant mt-0.5">{t("settings.integrations_desc", "Connection state and trust boundaries without sensitive token leakage.")}</p>
            </div>

            <dl className="settingsFacts grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
              <Fact label={t("settings.cloud_connection", "Cloud connection")} status={connectionStatus(console.session?.cloud_connected)} value={connectionLabel(console.session?.cloud_connected, t)} />
              <Fact label={t("settings.agent_connection", "Agent connection")} status={connectionStatus(console.session?.agent_connected)} value={connectionLabel(console.session?.agent_connected, t)} />
              <Fact label={t("settings.cloud_authority", "Cloud authority")} value={settings.cloud_authority || t("common.missing", "Missing")} />
              <Fact label={t("settings.agent_tls_pin", "Agent TLS pin")} status={settings.agent_tls_pinned ? "healthy" : "unknown"} value={settings.agent_tls_pinned ? t("status.configured", "Configured") : t("common.missing", "Missing")} />
            </dl>
          </div>
        ) : null}

        {tab === "system" ? (
          <div className="space-y-6">
            <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-6">
              <div className="border-b border-outline-variant/20 pb-4">
                <h3 className="font-headline-md text-lg font-bold text-on-surface">{t("settings.tab_system", "System")}</h3>
                <p className="text-xs text-on-surface-variant mt-0.5">{t("settings.system_desc", "Build and installed asset facts reported by the Local binary.")}</p>
              </div>

              <dl className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
                <Fact label={t("settings.cli_ui_version", "CLI/UI version")} value={settings.version || t("common.missing", "Missing")} />
                <Fact label={t("settings.revision", "Revision")} mono value={settings.revision || t("common.missing", "Missing")} />
                <Fact label={t("settings.go_version", "Go version")} value={settings.go_version || t("common.missing", "Missing")} />
                <Fact label={t("settings.installed_ui_assets", "Installed UI assets")} value={settings.ui_assets || t("common.missing", "Missing")} />
              </dl>
            </div>

            <details className="capabilityLimits bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
              <summary className="font-headline-md text-sm font-bold text-on-surface cursor-pointer">{t("settings.capability_limits", "Capability limits")}</summary>
              {settings.backend_gaps.length ? (
                <dl className="space-y-2 mt-4">
                  {settings.backend_gaps.map((gap) => (
                    <div key={gap.capability} className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 text-xs">
                      <dt className="text-on-surface font-semibold inline">
                        <span>{gap.capability}</span>:{" "}
                      </dt>
                      <dd className="inline text-on-surface-variant">{gap.status}. {t("settings.roadmap_label", { roadmap: gap.roadmap }, `Roadmap: ${gap.roadmap}.`)}</dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <Empty text={t("settings.no_capability_limits", "No backend capability limits were reported.")} />
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

function connectionLabel(value?: string, t?: (key: string, fb?: string) => string) {
  if (value === "ok") return t ? t("status.connected", "Connected") : "Connected";
  if (value === "failed") return t ? t("status.unavailable", "Unavailable") : "Unavailable";
  if (value === "not connected") return t ? t("status.disconnected", "Not connected") : "Not connected";
  return t ? t("status.unknown", "Unknown") : "Unknown";
}

function connectionStatus(value?: string) {
  return value === "ok" ? "healthy" : value === "failed" ? "unavailable" : "unknown";
}
