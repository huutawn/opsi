import type { BuildJob, BuildRecord, DeploymentJob, GitHubBinding, GitHubInstallation, GitHubRepository, PlacementFacts, ServiceRecord, TopologyAssignment, TopologyPlan } from "@/lib/contracts/registry";

export type ApplicationFacts = {
  service: ServiceRecord;
  binding?: GitHubBinding;
  installation?: GitHubInstallation;
  repository?: GitHubRepository;
  assignments: TopologyAssignment[];
  assignment?: TopologyAssignment;
  runtime?: PlacementFacts["runtimes"][number];
  nodes: PlacementFacts["nodes"];
  buildJobs: BuildJob[];
  buildRecords: BuildRecord[];
  latestBuildJob?: BuildJob;
  latestBuildRecord?: BuildRecord;
  latestDeployment?: DeploymentJob;
  latestExposure?: DeploymentJob;
};

export function applicationFacts(input: {
  services: ServiceRecord[];
  bindings: GitHubBinding[];
  installations: GitHubInstallation[];
  repositories: GitHubRepository[];
  topology: TopologyPlan | null;
  placement: PlacementFacts | null;
  buildJobs: Record<string, BuildJob[]>;
  buildRecords: BuildRecord[];
  deployments: DeploymentJob[];
  exposures: DeploymentJob[];
  environmentID?: string;
}) {
  return input.services.filter((service) => service.type === "application").map((service): ApplicationFacts => {
    const binding = input.bindings.find((item) => item.service_id === service.id && item.status === "active");
    const assignments = (input.topology?.assignments ?? []).filter((item) => item.service_key === service.name);
    const assignment = input.environmentID ? assignments.find((item) => item.environment_id === input.environmentID) : assignments.length === 1 ? assignments[0] : undefined;
    const runtime = input.placement?.runtimes.find((item) => item.id === assignment?.runtime_id);
    const records = newest(input.buildRecords.filter((item) => item.service_id === service.id && item.service_key === service.name && item.build.status === "succeeded"));
    const jobs = newest(input.buildJobs[service.id] ?? []);
    const deployments = newest(input.deployments.filter((item) => item.service_id === service.id));
    const exposures = newest(input.exposures.filter((item) => item.service_id === service.id));
    return {
      service,
      binding,
      installation: input.installations.find((item) => item.installation_id === binding?.installation_id),
      repository: input.repositories.find((item) => item.repository_id === binding?.repository_id),
      assignments,
      assignment,
      runtime,
      nodes: input.placement?.nodes.filter((item) => item.runtime_id === assignment?.runtime_id) ?? [],
      buildJobs: jobs,
      buildRecords: records,
      latestBuildJob: jobs[0],
      latestBuildRecord: records[0],
      latestDeployment: deployments[0],
      latestExposure: exposures[0] ?? deployments.find((item) => item.exposure_spec),
    };
  });
}

export function placementLabel(facts: ApplicationFacts) {
  if (!facts.assignments.length) return "Unplaced";
  if (!facts.assignment) return `${facts.assignments.length} assignments`;
  return [facts.runtime?.name || facts.assignment.runtime_id, facts.nodes.length === 1 ? facts.nodes[0].id : facts.nodes.length > 1 ? `${facts.nodes.length} nodes` : ""].filter(Boolean).join(" · ");
}

export function buildState(facts: ApplicationFacts) {
  return facts.latestBuildJob?.status ?? (facts.latestBuildRecord ? "succeeded" : "not_built");
}

export function deploymentState(facts: ApplicationFacts) {
  return facts.latestDeployment?.rollout_state || facts.latestDeployment?.status || "not_deployed";
}

export function exactSourceSHA(facts: ApplicationFacts) {
  const job = facts.latestBuildJob;
  const record = facts.latestBuildRecord;
  if (!job) return record?.workload.sha;
  if (!record) return job.source.resolved_commit_sha;
  return job.created_at >= record.created_at ? job.source.resolved_commit_sha : record.workload.sha;
}

export function acceptedDigest(facts: ApplicationFacts) {
  return facts.latestBuildRecord?.build.oci_digest;
}

function newest<T extends { created_at: string }>(items: T[]) {
  return [...items].sort((a, b) => b.created_at.localeCompare(a.created_at));
}
