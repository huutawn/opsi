"use client";

import { useState } from "react";
import type { BuildJob, BuildRecord } from "@/lib/contracts/registry";
import { buildFailure, buildFailureCategory } from "@/lib/presentation/build";

export function BuildJobFacts({ job }: { job: BuildJob }) {
  const failure = job.status === "failed" ? buildFailure(job.failure_code, job.failure_message_redacted) : null;
  return <div className="buildFacts"><div className="buildStateHeading"><span><small>BuildJob</small><code>{job.id}</code></span><b data-state={job.status}>{job.status}</b></div><dl><Fact label="Created" value={displayTime(job.created_at)} /><Fact label="Selected ref" value={job.source.selected_ref} mono /><Fact label="Exact source SHA" value={job.source.resolved_commit_sha} mono /><Fact label="Requested strategy" value={strategyName(job.requested_build_strategy)} /><Fact label="Resolved strategy" value={strategyName(job.resolved_build_strategy)} /><Fact label="Application root" value={job.source.application_root} mono /><Fact label="Build context" value={job.source.build_context} mono />{job.dockerfile_path ? <Fact label="Dockerfile" value={job.dockerfile_path} mono /> : null}</dl>{failure ? <div className="buildFailure" role="alert"><b>{buildFailureCategory(job.failure_code)} · {failure.title}</b><p>{job.failure_message_redacted || failure.action}</p><small>{failure.action}</small>{job.failure_code ? <code>{job.failure_code}</code> : null}</div> : null}</div>;
}

export function BuildRecordFacts({ record }: { record: BuildRecord }) {
  const [copyState, setCopyState] = useState("Copy digest");
  const processes = record.build.builder?.processes?.map((process) => process.type).join(", ");
  const builder = [record.build.builder_identity, record.build.builder_version].filter(Boolean).join(" · ");
  async function copyDigest() {
    try {
      await navigator.clipboard.writeText(record.build.oci_digest);
      setCopyState("Copied");
    } catch {
      setCopyState("Copy failed");
    }
  }
  return <div className="buildFacts acceptedBuild"><div className="buildStateHeading"><span><small>Accepted BuildRecord</small><code>{record.id}</code></span><b data-state="succeeded">succeeded</b></div><dl><Fact label="Created" value={displayTime(record.created_at)} /><Fact label="Exact commit SHA" value={record.workload.sha} mono /><Fact label="Strategy" value={strategyName(record.build.build_strategy)} /><Fact label="Registry repository" value={record.build.oci_repository} mono /><div className="digestFact"><dt>Immutable image digest</dt><dd><code>{record.build.oci_digest}</code><button onClick={() => void copyDigest()} type="button">{copyState}</button></dd></div><Fact label="Builder" value={builder || "Not reported"} />{record.build.builder?.builder_image ? <Fact label="Builder image" value={record.build.builder.builder_image} mono /> : null}{record.build.builder?.lifecycle_version ? <Fact label="Lifecycle" value={record.build.builder.lifecycle_version} mono /> : null}{record.build.builder?.pack_version ? <Fact label="Pack" value={record.build.builder.pack_version} mono /> : null}{processes ? <Fact label="Detected processes" value={processes} /> : null}</dl>{record.build.build_strategy === "dockerfile" && /buildkit/i.test(builder) ? <details className="technicalDetails"><summary>Technical executor</summary><code>Executor: {builder}</code></details> : null}</div>;
}

export function Fact({ label, value, mono = false }: { label: string; value?: string | number; mono?: boolean }) {
  return <div><dt>{label}</dt><dd className={mono ? "monoWrap" : undefined}>{value === undefined || value === "" ? "Not reported" : value}</dd></div>;
}

export function strategyName(value?: string) {
  return value === "buildpack" ? "Cloud Native Buildpacks" : value === "dockerfile" ? "Dockerfile" : value === "auto" ? "Automatic" : "Not resolved";
}

function displayTime(value?: string) {
  if (!value) return "Not reported";
  const time = new Date(value);
  return Number.isNaN(time.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(time);
}
