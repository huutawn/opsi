"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BootstrapSession, SSHHostKeyObservation, TimelineEvent } from "@/lib/contracts/registry";

export function BootstrapDialog({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated: () => Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [method, setMethod] = useState("command");
  const [host, setHost] = useState("");
  const [port, setPort] = useState(22);
  const [probe, setProbe] = useState<SSHHostKeyObservation | null>(null);
  const [probing, setProbing] = useState(false);
  const [probeError, setProbeError] = useState<string | null>(null);
  const [confirmingRotation, setConfirmingRotation] = useState(false);
  const [rotationConfirmed, setRotationConfirmed] = useState(false);
  const client = useMemo(() => new LocalClient(), []);

  const projectID = console.state.project?.id ?? "";

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  async function triggerProbe(targetHost: string, targetPort: number) {
    const trimmed = targetHost.trim();
    if (!trimmed || !projectID) return;
    setProbing(true);
    setProbeError(null);
    setProbe(null);
    setRotationConfirmed(false);
    try {
      const result = await client.probeSSHHostKey(projectID, { public_host: trimmed, ssh_port: targetPort });
      setProbe(result);
      if (result.status === "confirmed") {
        setRotationConfirmed(true);
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Failed to probe SSH host key";
      setProbeError(message);
    } finally {
      setProbing(false);
    }
  }

  async function handleConfirmRotation() {
    if (!probe || !projectID) return;
    setConfirmingRotation(true);
    try {
      await client.confirmSSHHostKey(projectID, probe.id, { fingerprint: probe.fingerprint });
      setRotationConfirmed(true);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Failed to confirm host key rotation";
      setProbeError(message);
    } finally {
      setConfirmingRotation(false);
    }
  }

  const isSSH = method !== "command";
  const sshBlocked = isSSH && (probing || !probe || (probe.trust_state === "changed" && !rotationConfirmed));

  return (
    <dialog
      aria-describedby="bootstrap-description"
      aria-labelledby="bootstrap-title"
      className="fixed inset-0 z-50 m-auto flex max-h-[90vh] w-full max-w-xl flex-col gap-5 overflow-y-auto rounded-2xl border border-outline-variant/30 bg-surface-container-low p-6 text-on-surface shadow-2xl backdrop:bg-background/80 backdrop:backdrop-blur-sm"
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="mb-1 block text-xs font-bold uppercase tracking-wider text-primary">Deploy · Target</span>
          <h2 className="text-xl font-bold" id="bootstrap-title">Connect Server</h2>
          <p className="mt-1 text-xs text-on-surface-variant" id="bootstrap-description">
            {method === "command"
              ? "Generate a one-time command, run it on the VPS, then follow progress until Ready."
              : "Verify remote SSH host-key identity and start automated bootstrap."}
          </p>
        </div>
        <button
          aria-label="Close connect server dialog"
          autoFocus
          className="flex min-h-10 min-w-10 items-center justify-center rounded-lg p-2 text-on-surface-variant hover:bg-surface-container-highest hover:text-on-surface"
          onClick={onClose}
          type="button"
        >
          <Icon className="text-[20px]" name="close" />
        </button>
      </div>

      <ol aria-label="Connect Server steps" className="grid grid-cols-3 gap-2 rounded-xl border border-outline-variant/15 bg-surface-container p-3 text-xs">
        <li className="font-bold text-primary">1 · {method === "command" ? "Generate" : "Verify Host"}</li>
        <li className="text-on-surface-variant">2 · {method === "command" ? "Run on VPS" : "Authenticate"}</li>
        <li className="text-on-surface-variant">3 · Wait for Ready</li>
      </ol>

      <form
        className="space-y-4"
        onSubmit={(event) => {
          void console.actions.addServer(event, onCreated);
          onClose();
        }}
      >
        <label className="grid gap-1.5 text-xs text-on-surface-variant">
          Role
          <select aria-label="Role" className="field min-h-10" defaultValue="first_server" name="role" required>
            <option value="first_server">First server</option>
            <option value="worker">Worker</option>
          </select>
        </label>

        <label className="grid gap-1.5 text-xs text-on-surface-variant">
          Server IP or hostname
          <input
            aria-label="Server IP or hostname"
            autoComplete="off"
            className="field min-h-10"
            name="public_host"
            onBlur={(e) => {
              const val = e.target.value;
              setHost(val);
              if (isSSH && val.trim()) void triggerProbe(val, port);
            }}
            onChange={(e) => setHost(e.target.value)}
            placeholder="203.0.113.10"
            required
            spellCheck={false}
            value={host}
          />
        </label>

        <fieldset className="space-y-3 rounded-xl border border-outline-variant/15 bg-surface-container/60 p-4">
          <legend className="px-1 text-xs font-bold uppercase tracking-wider">Bootstrap method</legend>
          <label className="flex items-start gap-3 rounded-xl border border-outline-variant/20 bg-surface-container-high p-3">
            <input
              aria-label="Run bootstrap command"
              checked={method === "command"}
              className="mt-1"
              name="auth_method"
              onChange={() => setMethod("command")}
              type="radio"
              value="command"
            />
            <span>
              <strong className="block text-xs">Run bootstrap command</strong>
              <small className="text-on-surface-variant">Recommended. Scoped one-time execution on the target.</small>
            </span>
          </label>

          <details className="text-xs text-on-surface-variant" open={isSSH}>
            <summary className="flex min-h-10 cursor-pointer items-center font-medium">Advanced: Bootstrap over SSH</summary>
            <div className="space-y-3 pt-2">
              {[["password", "SSH Password"], ["private_key", "SSH Private Key"]].map(([value, label]) => (
                <label className="flex items-start gap-3 rounded-xl border border-outline-variant/20 bg-surface-container-high p-3" key={value}>
                  <input
                    aria-label={label}
                    checked={method === value}
                    className="mt-1"
                    name="auth_method"
                    onChange={() => {
                      setMethod(value);
                      if (host.trim() && !probe && !probing) {
                        void triggerProbe(host, port);
                      }
                    }}
                    type="radio"
                    value={value}
                  />
                  <span>{label}</span>
                </label>
              ))}

              {isSSH && (
                <div className="space-y-3 pt-2">
                  <div className="grid grid-cols-2 gap-3">
                    <label className="grid gap-1">
                      SSH port
                      <input
                        aria-label="SSH port"
                        className="field"
                        defaultValue="22"
                        max="65535"
                        min="1"
                        name="ssh_port"
                        onBlur={(e) => {
                          const p = Number(e.target.value) || 22;
                          setPort(p);
                          if (host.trim()) void triggerProbe(host, p);
                        }}
                        required
                        type="number"
                      />
                    </label>
                    <label className="grid gap-1">
                      SSH username
                      <input aria-label="SSH username" autoComplete="username" className="field" defaultValue="root" name="ssh_username" required />
                    </label>
                  </div>

                  {probe && <input name="ssh_host_key_probe_id" type="hidden" value={probe.id} />}

                  {/* Probing feedback section */}
                  <div aria-live="polite" className="rounded-xl border border-outline-variant/20 bg-surface-container p-3">
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span>SSH Host-Key Identity</span>
                      {host.trim() && (
                        <button
                          className="text-xs text-primary underline hover:text-primary/80 disabled:opacity-50"
                          disabled={probing}
                          onClick={() => void triggerProbe(host, port)}
                          type="button"
                        >
                          {probing ? "Probing…" : "Re-probe"}
                        </button>
                      )}
                    </div>

                    {probing && (
                      <div aria-busy="true" className="mt-2 flex items-center gap-2 text-xs text-on-surface-variant">
                        <span className="h-3 w-3 animate-spin rounded-full border-2 border-primary border-t-transparent" />
                        <span>Verifying SSH host key at {host}:{port}…</span>
                      </div>
                    )}

                    {probeError && !probing && (
                      <div className="mt-2 rounded-lg border border-error/30 bg-error/10 p-2.5 text-xs text-error">
                        <p className="font-semibold">Probe failed</p>
                        <p className="mt-0.5">{probeError}</p>
                      </div>
                    )}

                    {probe && !probing && (
                      <div className="mt-2 space-y-2 text-xs">
                        {probe.trust_state === "first_seen" && (
                          <div className="rounded-lg border border-primary/30 bg-primary/10 p-2.5 text-on-surface">
                            <div className="flex items-center justify-between">
                              <span className="font-bold text-primary">First Connection (TOFU)</span>
                              <span className="font-mono text-[10px] text-on-surface-variant">{probe.algorithm}</span>
                            </div>
                            <p className="mt-1 font-mono text-[11px] break-all">{probe.fingerprint}</p>
                            <p className="mt-1.5 text-[11px] text-on-surface-variant">
                              Opsi will automatically pin this host key for this project on first connection.
                            </p>
                          </div>
                        )}

                        {probe.trust_state === "matched" && (
                          <div className="rounded-lg border border-status-ready/30 bg-status-ready/10 p-2.5 text-on-surface">
                            <div className="flex items-center justify-between">
                              <span className="font-bold text-status-ready">Host Key Verified</span>
                              <span className="font-mono text-[10px] text-on-surface-variant">{probe.algorithm}</span>
                            </div>
                            <p className="mt-1 font-mono text-[11px] break-all">{probe.fingerprint}</p>
                            <p className="mt-1.5 text-[11px] text-on-surface-variant">
                              Matches the pinned host key for this project.
                            </p>
                          </div>
                        )}

                        {probe.trust_state === "changed" && (
                          <div className="rounded-lg border border-status-warning/40 bg-status-warning/10 p-3 text-on-surface">
                            <div className="flex items-center gap-1.5 font-bold text-status-warning">
                              <Icon className="text-base" name="warning" />
                              <span>Security Notice: Host Key Changed</span>
                            </div>
                            <p className="mt-1 text-[11px] text-on-surface-variant">
                              The target host key does not match the pinned project identity.
                            </p>
                            <div className="mt-2 space-y-1 font-mono text-[11px]">
                              <p className="text-error">Previous: {probe.previous_fingerprint || "(unrecorded)"}</p>
                              <p className="text-status-ready">Observed: {probe.fingerprint}</p>
                            </div>
                            <div className="mt-3 flex items-center justify-between border-t border-outline-variant/20 pt-2">
                              <span className="text-[11px] text-on-surface-variant">
                                {rotationConfirmed ? "Key rotation confirmed." : "Confirmation required before bootstrap."}
                              </span>
                              {!rotationConfirmed && (
                                <Button
                                  disabled={confirmingRotation}
                                  onClick={() => void handleConfirmRotation()}
                                  size="sm"
                                  type="button"
                                  variant="outline"
                                >
                                  {confirmingRotation ? "Confirming…" : "Confirm rotation"}
                                </Button>
                              )}
                            </div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          </details>
        </fieldset>

        <p className="rounded-xl border border-outline-variant/10 bg-surface-container/40 p-3 text-xs text-on-surface-variant">
          The one-time credential is requested only at final confirmation and is never logged.
        </p>

        <div className="flex justify-end gap-3 border-t border-outline-variant/20 pt-3">
          <Button onClick={onClose} type="button" variant="outline">
            Cancel
          </Button>
          <Button disabled={sshBlocked} type="submit" variant="primary">
            {method === "command" ? "Generate bootstrap command" : "Connect server over SSH"}
          </Button>
        </div>
      </form>
    </dialog>
  );
}

export function BootstrapCommand({ command }: { command: string }) {
  const [copyState, setCopyState] = useState("Copy command");
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopyState("Copied");
    } catch {
      setCopyState("Copy failed");
    }
  }
  return (
    <section aria-labelledby="bootstrap-command-title" className="bootstrapCommand">
      <div>
        <div>
          <p className="eyebrow">One-time scoped command</p>
          <h4 id="bootstrap-command-title">Copy, then run on the VPS as root</h4>
        </div>
        <button className="primary" onClick={() => void copy()} type="button">
          {copyState}
        </button>
      </div>
      <code>{command}</code>
      <p role="status">Waiting for server. Refresh restores lifecycle facts, not this one-time command.</p>
    </section>
  );
}

export function BootstrapProgress({
  events,
  session,
  console,
}: {
  events: TimelineEvent[];
  session: BootstrapSession;
  console?: ConsoleController;
}) {
  const [showRotationDialog, setShowRotationDialog] = useState(false);
  const recentEvents = events.slice(-4).reverse();
  const latest = events.at(-1);
  const progress = latest?.progress_percent ?? Math.min(100, Math.max(0, (session.checkpoint?.next_step_index ?? 0) * 25));

  const isWaitingConfirmation = session.status === "waiting_host_key_confirmation";

  return (
    <section
      aria-busy={session.status !== "completed" && !isWaitingConfirmation}
      aria-labelledby="bootstrap-progress-title"
      aria-live="polite"
      className="space-y-4 border border-outline-variant/30 bg-surface-container-low p-4 sm:p-5"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="eyebrow">Server connection</p>
          <h3 className="text-base font-bold text-on-surface" id="bootstrap-progress-title">
            Connecting {session.public_host || "server"}
          </h3>
          <p className="mt-1 text-xs text-on-surface-variant">
            Session {session.id} · attempt {session.attempt_count || 1} of {session.max_attempts || 1}
          </p>
        </div>
        <StatusBadge value={session.status} />
      </div>

      <div className="space-y-1.5">
        <div className="flex justify-between text-xs text-on-surface-variant">
          <span>{latest?.message_redacted || (isWaitingConfirmation ? "Waiting for host key confirmation" : "Waiting for the bootstrap worker.")}</span>
          <span>{progress}%</span>
        </div>
        <progress aria-label="Server bootstrap progress" className="h-2 w-full accent-primary" max={100} value={progress} />
      </div>

      {isWaitingConfirmation && (
        <div className="rounded-xl border border-status-warning/40 bg-status-warning/10 p-4 text-on-surface">
          <div className="flex items-start justify-between gap-3">
            <div>
              <div className="flex items-center gap-1.5 font-bold text-status-warning">
                <Icon className="text-base" name="warning" />
                <span>SSH Host-Key Mismatch Detected</span>
              </div>
              <p className="mt-1 text-xs text-on-surface-variant">
                The target server presented a different SSH host key than the pinned project identity. Bootstrap is paused at checkpoint step {session.checkpoint?.last_completed_step || "initial"}.
              </p>
            </div>
            {console && (
              <Button onClick={() => setShowRotationDialog(true)} size="sm" type="button" variant="primary">
                Review & Resume
              </Button>
            )}
          </div>
        </div>
      )}

      {recentEvents.length > 0 && (
        <ol aria-label="Latest bootstrap events" className="space-y-2 border-t border-outline-variant/20 pt-3">
          {recentEvents.map((event) => (
            <li className="flex items-start justify-between gap-4 text-xs" key={event.id}>
              <span className="text-on-surface">{event.message_redacted}</span>
              <span className="shrink-0 font-mono text-on-surface-variant">{event.progress_percent}%</span>
            </li>
          ))}
        </ol>
      )}

      {showRotationDialog && console && (
        <HostKeyRotationResumeDialog
          console={console}
          onClose={() => setShowRotationDialog(false)}
          session={session}
        />
      )}
    </section>
  );
}

export function HostKeyRotationResumeDialog({
  console,
  session,
  onClose,
}: {
  console: ConsoleController;
  session: BootstrapSession;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [authMethod, setAuthMethod] = useState(session.auth_method || "password");
  const client = useMemo(() => new LocalClient(), []);
  const [username, setUsername] = useState("root");
  const [credential, setCredential] = useState("");
  const [verifiedConsent, setVerifiedConsent] = useState(false);
  const [probe, setProbe] = useState<SSHHostKeyObservation | null>(null);
  const [probing, setProbing] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [resuming, setResuming] = useState(false);

  const projectID = console.state.project?.id ?? "";
  const host = session.public_host ?? "";
  const port = 22;

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  useEffect(() => {
    async function loadProbe() {
      if (!host || !projectID) return;
      setProbing(true);
      setError(null);
      try {
        const result = await client.probeSSHHostKey(projectID, { public_host: host, ssh_port: port });
        setProbe(result);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Failed to probe host key");
      } finally {
        setProbing(false);
      }
    }
    void loadProbe();
  }, [client, host, port, projectID]);

  async function handleConfirmAndResume(e: React.FormEvent) {
    e.preventDefault();
    if (!probe || !projectID || !credential.trim()) return;

    setResuming(true);
    setError(null);
    try {
      // Step 1: Confirm rotation if not confirmed yet
      if (probe.status !== "confirmed") {
        await client.confirmSSHHostKey(projectID, probe.id, { fingerprint: probe.fingerprint });
      }
      // Step 2: Resume session with new credentials
      await console.actions.resumeBootstrap(session.id, probe.id, authMethod, credential.trim(), username);
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to confirm and resume bootstrap");
    } finally {
      setResuming(false);
    }
  }

  return (
    <dialog
      aria-describedby="rotation-desc"
      aria-labelledby="rotation-title"
      className="fixed inset-0 z-50 m-auto flex max-h-[90vh] w-full max-w-lg flex-col gap-4 overflow-y-auto rounded-2xl border border-outline-variant/30 bg-surface-container-low p-6 text-on-surface shadow-2xl backdrop:bg-background/80 backdrop:backdrop-blur-sm"
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-3">
        <div>
          <span className="mb-0.5 block text-xs font-bold uppercase tracking-wider text-status-warning">Security · Key Rotation</span>
          <h2 className="text-lg font-bold" id="rotation-title">Confirm Host Key & Resume</h2>
          <p className="mt-0.5 text-xs text-on-surface-variant" id="rotation-desc">
            Review the updated host key for {host}:{port} and re-enter SSH credentials to resume bootstrap.
          </p>
        </div>
        <button
          aria-label="Close dialog"
          autoFocus
          className="flex min-h-8 min-w-8 items-center justify-center rounded-lg p-1.5 text-on-surface-variant hover:bg-surface-container-highest hover:text-on-surface"
          onClick={onClose}
          type="button"
        >
          <Icon className="text-lg" name="close" />
        </button>
      </div>

      <form className="space-y-4 text-xs" onSubmit={(e) => void handleConfirmAndResume(e)}>
        {/* Step 1: Host Key Inspection */}
        <div className="space-y-2 rounded-xl border border-outline-variant/20 bg-surface-container p-3">
          <div className="flex items-center justify-between font-semibold">
            <span>Observed SSH Host Key</span>
            {probing && <span className="text-[11px] text-on-surface-variant">Probing…</span>}
          </div>

          {probing && (
            <div aria-busy="true" className="flex items-center gap-2 py-2 text-on-surface-variant">
              <span className="h-3 w-3 animate-spin rounded-full border-2 border-primary border-t-transparent" />
              <span>Connecting to {host}…</span>
            </div>
          )}

          {probe && !probing && (
            <div className="space-y-1.5">
              <div className="font-mono text-[11px]">
                {probe.previous_fingerprint && <p className="text-error">Previous: {probe.previous_fingerprint}</p>}
                <p className="text-status-ready">New key: {probe.fingerprint}</p>
                <p className="text-[10px] text-on-surface-variant">Algorithm: {probe.algorithm}</p>
              </div>
            </div>
          )}

          <label className="mt-2 flex items-start gap-2 pt-1 font-medium text-on-surface">
            <input
              checked={verifiedConsent}
              className="mt-0.5"
              onChange={(e) => setVerifiedConsent(e.target.checked)}
              required
              type="checkbox"
            />
            <span>I verify that this new host key fingerprint is authentic and matches the server.</span>
          </label>
        </div>

        {/* Step 2: Re-enter Credentials */}
        <div className="space-y-3 rounded-xl border border-outline-variant/20 bg-surface-container p-3">
          <span className="font-semibold">Re-enter One-Time SSH Credential</span>
          <div className="flex gap-4">
            <label className="flex items-center gap-2">
              <input
                checked={authMethod === "password"}
                name="resume_auth_method"
                onChange={() => setAuthMethod("password")}
                type="radio"
                value="password"
              />
              <span>Password</span>
            </label>
            <label className="flex items-center gap-2">
              <input
                checked={authMethod === "private_key"}
                name="resume_auth_method"
                onChange={() => setAuthMethod("private_key")}
                type="radio"
                value="private_key"
              />
              <span>Private Key</span>
            </label>
          </div>

          <label className="grid gap-1">
            SSH Username
            <input
              className="field"
              onChange={(e) => setUsername(e.target.value)}
              required
              type="text"
              value={username}
            />
          </label>

          {authMethod === "password" ? (
            <label className="grid gap-1">
              SSH Password
              <input
                autoComplete="current-password"
                className="field"
                onChange={(e) => setCredential(e.target.value)}
                placeholder="Enter password"
                required
                type="password"
                value={credential}
              />
            </label>
          ) : (
            <label className="grid gap-1">
              SSH Private Key
              <textarea
                className="field font-mono text-[11px]"
                onChange={(e) => setCredential(e.target.value)}
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
                required
                rows={4}
                value={credential}
              />
            </label>
          )}
        </div>

        {error && (
          <div className="rounded-lg border border-error/30 bg-error/10 p-2.5 text-error">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2 border-t border-outline-variant/20 pt-3">
          <Button onClick={onClose} type="button" variant="outline">
            Cancel
          </Button>
          <Button
            disabled={!verifiedConsent || !credential.trim() || probing || resuming}
            type="submit"
            variant="primary"
          >
            {resuming ? "Resuming…" : "Confirm & Resume Session"}
          </Button>
        </div>
      </form>
    </dialog>
  );
}
