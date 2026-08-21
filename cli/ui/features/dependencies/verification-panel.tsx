"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type {
  ApplicationDependency,
  VerificationRun,
  VerifyDependencyRequest,
} from "@/lib/contracts/registry";

type Props = {
  applicationID: string;
  dependency: ApplicationDependency;
  deploymentJobID?: string;
  environmentID?: string;
  onVerified?: (run: VerificationRun) => void;
  projectID: string;
};

export function DependencyVerificationPanel({
  applicationID,
  dependency,
  deploymentJobID,
  environmentID,
  onVerified,
  projectID,
}: Props) {
  const client = useMemo(() => new LocalClient(), []);
  const [run, setRun] = useState<VerificationRun | null>(null);
  const [loading, setLoading] = useState(true);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState<{ code?: string; message: string } | null>(null);

  useEffect(() => {
    let active = true;
    async function fetchVerification() {
      setLoading(true);
      setError(null);
      try {
        const res = await client.dependencyVerification(
          projectID,
          dependency.logical_name,
          applicationID,
          environmentID
        );
        if (active) setRun(res.run);
      } catch {
        // 404 is normal if not run yet
        if (active) setRun(null);
      } finally {
        if (active) setLoading(false);
      }
    }
    void fetchVerification();
    return () => {
      active = false;
    };
  }, [applicationID, client, dependency.logical_name, environmentID, projectID]);

  async function triggerVerification() {
    if (!deploymentJobID && !run?.deployment_job_id) {
      setError({
        message: "No active deployment found to verify against. Deploy the application first.",
      });
      return;
    }
    setVerifying(true);
    setError(null);
    try {
      const body: VerifyDependencyRequest = {
        dependency_logical_name: dependency.logical_name,
        deployment_job_id: deploymentJobID || run?.deployment_job_id || "",
        consumer_contract: dependency.verification_contract,
      };
      const res = await client.verifyDependency(
        projectID,
        body,
        applicationID,
        environmentID,
        crypto.randomUUID()
      );
      setRun(res.run);
      if (onVerified) onVerified(res.run);
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      setError({
        code: apiErr.code,
        message: apiErr.message || "Failed to trigger dependency verification.",
      });
    } finally {
      setVerifying(false);
    }
  }

  const overall = run?.overall_status || "NOT_RUN";

  return (
    <div className="bg-surface-container rounded-xl p-4 border border-outline-variant/20 space-y-4 text-xs">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Icon name="verified_user" className="text-primary text-[18px]" />
          <h4 className="font-headline-md text-sm font-semibold text-on-surface">
            Layered Post-Deploy Verification
          </h4>
        </div>
        <div className="flex items-center gap-3">
          <StatusBadge
            label={overall.replaceAll("_", " ")}
            value={
              overall === "VERIFIED"
                ? "healthy"
                : overall === "PARTIALLY_VERIFIED"
                ? "in_progress"
                : overall === "FAILED"
                ? "failed"
                : overall === "STALE"
                ? "in_progress"
                : "unknown"
            }
          />
          <Button
            disabled={verifying || (!deploymentJobID && !run?.deployment_job_id)}
            onClick={() => void triggerVerification()}
            size="sm"
            variant="secondary"
          >
            <Icon name={verifying ? "sync" : "play_arrow"} className={`text-[14px] ${verifying ? "animate-spin" : ""}`} />
            <span>{verifying ? "Verifying…" : overall === "NOT_RUN" ? "Verify Dependency" : "Verify Again"}</span>
          </Button>
        </div>
      </div>

      {error ? (
        <div className="p-3 bg-error-container/20 border border-status-failed/30 rounded-lg text-xs text-status-failed flex items-start gap-2">
          <Icon name="error" className="text-[16px] shrink-0 mt-0.5" />
          <span>{error.message}</span>
        </div>
      ) : null}

      {loading ? (
        <div className="py-4 text-center text-on-surface-variant flex items-center justify-center gap-2">
          <Icon name="sync" className="animate-spin text-[16px]" />
          <span>Loading verification status…</span>
        </div>
      ) : run ? (
        <div className="space-y-3">
          {/* Summary Explanation */}
          <div className="p-3 bg-surface-container-high/60 rounded-lg border border-outline-variant/15 text-[11px] text-on-surface-variant">
            {overall === "VERIFIED" ? (
              <p className="text-status-ready font-medium">
                All 5 verification layers succeeded: Provider is healthy, contract resolved, connection established, consumer workload ready, and application-level assertion returned expected status.
              </p>
            ) : overall === "PARTIALLY_VERIFIED" ? (
              <p className="text-status-warning font-medium">
                Infrastructure, dependency contract, and network connectivity were verified. No application-level dependency assertion is configured.
              </p>
            ) : overall === "FAILED" ? (
              <p className="text-status-failed font-medium">
                {run.consumer_assertion.status === "FAILED"
                  ? "Opsi can reach the dependency using the declared contract, but the application-level assertion failed."
                  : `Verification failed at layer: ${run.failure_code || "Check details below"}`}
              </p>
            ) : overall === "STALE" ? (
              <p className="text-status-warning font-medium">
                Stale verification. Underlying configuration, binding, or deployment state has changed since this run. Verify again.
              </p>
            ) : null}
          </div>

          {/* 5 Layer Cards */}
          <div className="space-y-2">
            {/* Layer 1: Provider Health */}
            <div className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 flex items-center justify-between">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 font-semibold text-on-surface">
                  <Icon name="dns" className="text-[15px] text-primary" />
                  <span>1. Upstream Provider Health</span>
                </div>
                <p className="text-[10px] text-on-surface-variant">
                  Kind: {run.provider_health.provider_kind} · Target: {run.provider_health.provider_id || dependency.target_identity}
                  {run.provider_health.message ? ` — ${run.provider_health.message}` : ""}
                </p>
              </div>
              <StatusBadge
                label={run.provider_health.status}
                value={run.provider_health.status === "HEALTHY" ? "healthy" : "failed"}
              />
            </div>

            {/* Layer 2: Contract Resolution */}
            <div className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 flex items-center justify-between">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 font-semibold text-on-surface">
                  <Icon name="handshake" className="text-[15px] text-primary" />
                  <span>2. Contract Resolution & Injection</span>
                </div>
                <p className="text-[10px] text-on-surface-variant">
                  Binding: {run.contract_resolution.binding_id || "Active"} · Injection complete: {run.contract_resolution.injection_complete ? "Yes" : "No"}
                  {run.contract_resolution.message ? ` — ${run.contract_resolution.message}` : ""}
                </p>
              </div>
              <StatusBadge
                label={run.contract_resolution.status}
                value={run.contract_resolution.status === "RESOLVED" ? "healthy" : "failed"}
              />
            </div>

            {/* Layer 3: Connection */}
            <div className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 flex items-center justify-between">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 font-semibold text-on-surface">
                  <Icon name="network_check" className="text-[15px] text-primary" />
                  <span>3. Protocol Connectivity Probe</span>
                </div>
                <p className="text-[10px] text-on-surface-variant">
                  Protocol: {run.connection.protocol || dependency.protocol}
                  {run.connection.latency_ms ? ` · Latency: ${run.connection.latency_ms}ms` : ""}
                  {run.connection.message ? ` — ${run.connection.message}` : ""}
                </p>
              </div>
              <StatusBadge
                label={run.connection.status}
                value={
                  run.connection.status === "VERIFIED"
                    ? "healthy"
                    : run.connection.status === "FAILED"
                    ? "failed"
                    : "unknown"
                }
              />
            </div>

            {/* Layer 4: Consumer Health */}
            <div className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 flex items-center justify-between">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 font-semibold text-on-surface">
                  <Icon name="memory" className="text-[15px] text-primary" />
                  <span>4. Consumer Workload Readiness</span>
                </div>
                <p className="text-[10px] text-on-surface-variant">
                  Ready pods: {run.consumer_health.ready_pods} / {run.consumer_health.total_pods}
                  {run.consumer_health.message ? ` — ${run.consumer_health.message}` : ""}
                </p>
              </div>
              <StatusBadge
                label={run.consumer_health.status}
                value={run.consumer_health.status === "HEALTHY" ? "healthy" : "failed"}
              />
            </div>

            {/* Layer 5: Consumer Assertion */}
            <div className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 flex items-center justify-between">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 font-semibold text-on-surface">
                  <Icon name="fact_check" className="text-[15px] text-primary" />
                  <span>5. Application-Level Consumer Assertion</span>
                </div>
                <p className="text-[10px] text-on-surface-variant">
                  {run.consumer_assertion.status === "NOT_CONFIGURED"
                    ? "No consumer HTTP assertion configured (required for full VERIFIED state)."
                    : `Path: ${run.consumer_assertion.assertion_path} · Status: ${run.consumer_assertion.status_code} (Expected: ${run.consumer_assertion.expected_code})`}
                  {run.consumer_assertion.message ? ` — ${run.consumer_assertion.message}` : ""}
                </p>
              </div>
              <StatusBadge
                label={run.consumer_assertion.status}
                value={
                  run.consumer_assertion.status === "VERIFIED"
                    ? "healthy"
                    : run.consumer_assertion.status === "FAILED"
                    ? "failed"
                    : "unknown"
                }
              />
            </div>
          </div>
        </div>
      ) : (
        <div className="py-3 text-on-surface-variant text-[11px]">
          Post-deploy verification has not been executed yet. Click &quot;Verify Dependency&quot; above to run all 5 verification layers against the deployed workload.
        </div>
      )}
    </div>
  );
}
