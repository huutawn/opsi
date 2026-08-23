import type { ApplicationDependency, DependencyInjectionMapping } from "@/lib/contracts/registry";

export type DependencyProposalEnvelope = {
  project_id: string;
  environment_id: string;
  application_id: string;
  provenance: {
    source_commit: string;
    application_root: string;
    analysis_inputs_hash: string;
  };
  candidate: {
    logical_name: string;
    dependency_kind: "managed_service" | "application";
    target_id: string;
    protocol: "postgres" | "redis" | "http";
    phase: "runtime" | "build";
    required: boolean;
    access_context?: "browser" | "server";
    strategy?: "same_origin" | "internal_http" | "public_http";
    path?: string;
    mappings?: DependencyInjectionMapping[];
    verification_contract?: { type: "consumer_http"; path: string; expected_status: number };
  };
  evidence?: Array<{ type: string; file: string; line: number; safe_excerpt?: string; symbol?: string; reason: string }>;
  confidence?: "HIGH" | "MEDIUM" | "LOW";
};

export type SourcePatchProposalEnvelope = {
  project_id: string;
  environment_id: string;
  application_id: string;
  provenance: {
    build_record_id: string;
    source_commit: string;
    application_root: string;
    analysis_inputs_hash: string;
    dependency_proposal_hash?: string;
  };
  rationale: { observed_source: string; opsi_facts: string; inference: string };
  files: Array<{ path: string; base_blob_sha: string; unified_diff: string }>;
  evidence?: Array<{ type: string; file: string; line: number; safe_excerpt?: string; symbol?: string; reason: string }>;
  impact?: { depends_on_unapplied_dependency_proposal?: boolean; alternative_configuration_only_solution?: boolean };
};

export function asApplicationDependency(proposal: DependencyProposalEnvelope): ApplicationDependency {
  const candidate = proposal.candidate;
  return {
    logical_name: candidate.logical_name,
    target_kind: candidate.dependency_kind,
    target_identity: candidate.target_id,
    protocol: candidate.protocol,
    injection_phase: candidate.phase,
    required: candidate.required,
    access_context: candidate.access_context,
    strategy: candidate.strategy,
    path: candidate.path,
    injection_mappings: candidate.mappings ?? [],
    verification_contract: candidate.verification_contract,
  };
}

export function isDependencyProposal(value: unknown): value is DependencyProposalEnvelope {
  const proposal = value as Partial<DependencyProposalEnvelope>;
  return Boolean(proposal?.candidate && proposal.project_id && proposal.environment_id && proposal.application_id && proposal.provenance);
}

export function isSourcePatchProposal(value: unknown): value is SourcePatchProposalEnvelope {
  const proposal = value as Partial<SourcePatchProposalEnvelope>;
  return Boolean(Array.isArray(proposal?.files) && proposal.project_id && proposal.environment_id && proposal.application_id && proposal.provenance && proposal.rationale);
}
