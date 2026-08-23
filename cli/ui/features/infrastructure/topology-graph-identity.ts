export const topologyHandleIDs = { source: "source", target: "target" } as const;

export type TopologyGraphAuthority = {
  applications: Array<{ id: string; name: string }>;
  managedResources?: Array<{ id: string }>;
  applicationBindings?: Array<{ sourceID: string; targetID: string }>;
  dependencies?: Array<{ logicalName: string; sourceID: string; targetID: string; targetKind: "application" | "managed_service" }>;
};

export function applicationTopologyNodeID(serviceName: string) {
  return `service:${serviceName}`;
}

export function applicationConnectionEdgeID(sourceServiceID: string, logicalTargetKey: string) {
  return `connection:${sourceServiceID}:${logicalTargetKey}`;
}

export function dependencyTopologyEdgeID(sourceServiceID: string, logicalName: string) {
  return `dep:${sourceServiceID}:${logicalName}`;
}

/**
 * A rendering-independent projection of factual topology identity. Canvas rendering
 * uses the same IDs and handles, so revisions only change this output when facts change.
 */
export function deriveTopologyGraphIdentity(authority: TopologyGraphAuthority) {
  const applicationsByID = new Map(authority.applications.map((application) => [application.id, application]));
  const nodes = [
    ...authority.applications.map((application) => ({
      id: applicationTopologyNodeID(application.name),
      sourceHandle: topologyHandleIDs.source,
      targetHandle: topologyHandleIDs.target,
    })),
    ...(authority.managedResources ?? []).map((resource) => ({
      id: `resource:${resource.id}`,
      sourceHandle: topologyHandleIDs.source,
      targetHandle: topologyHandleIDs.target,
    })),
  ].sort((left, right) => left.id.localeCompare(right.id));
  const edges = [
    ...(authority.applicationBindings ?? []).flatMap((binding) => {
      const source = applicationsByID.get(binding.sourceID);
      const target = applicationsByID.get(binding.targetID);
      return source && target
        ? [{
            id: applicationConnectionEdgeID(source.id, target.name),
            source: applicationTopologyNodeID(source.name),
            target: applicationTopologyNodeID(target.name),
            sourceHandle: topologyHandleIDs.source,
            targetHandle: topologyHandleIDs.target,
          }]
        : [];
    }),
    ...(authority.dependencies ?? []).flatMap((dependency) => {
      const source = applicationsByID.get(dependency.sourceID);
      const target = dependency.targetKind === "application"
        ? applicationsByID.get(dependency.targetID)
        : { name: dependency.targetID };
      return source && target
        ? [{
            id: dependencyTopologyEdgeID(source.id, dependency.logicalName),
            source: applicationTopologyNodeID(source.name),
            target: dependency.targetKind === "application" ? applicationTopologyNodeID(target.name) : `resource:${dependency.targetID}`,
            sourceHandle: topologyHandleIDs.source,
            targetHandle: topologyHandleIDs.target,
          }]
        : [];
    }),
  ].sort((left, right) => left.id.localeCompare(right.id));
  return { nodes, edges };
}
