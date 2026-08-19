package webhookrelay

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func (s *Server) runPreflight(ctx context.Context, projectID string, request deploymentv1.CreateRequest) (deploymentv1.PreflightResult, error) {
	result := deploymentv1.PreflightResult{
		Checks:      make([]deploymentv1.PreflightCheck, 0),
		GeneratedAt: s.clock(),
	}

	if request.BuildRecordID == "" || request.EnvironmentID == "" {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:request:invalid",
			Code:            "DEPLOYMENT_REQUEST_INVALID",
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         request.BuildRecordID,
			Message:         "build_record_id and environment_id are required",
			RemediationCode: deploymentv1.RemediationReviewConfiguration,
		})
		result.EvaluateStatus()
		result.SortChecks()
		return result, nil
	}

	// 1. BuildRecord
	record, err := s.BuildRecords.Get(ctx, projectID, request.BuildRecordID)
	if err != nil || record.ID == "" {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:build:" + request.BuildRecordID + ":BUILD_RECORD_MISSING",
			Code:            deploymentv1.CodeBuildRecordMissing,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         request.BuildRecordID,
			Message:         "accepted BuildRecord was not found",
			RemediationCode: deploymentv1.RemediationCreateBuild,
		})
		result.EvaluateStatus()
		result.SortChecks()
		return result, nil
	}
	if record.ProjectID != projectID {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:build:" + record.ServiceKey + ":BUILD_RECORD_MISSING",
			Code:            deploymentv1.CodeBuildRecordMissing,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         record.ServiceKey,
			Message:         "BuildRecord belongs to another project",
			RemediationCode: deploymentv1.RemediationCreateBuild,
		})
	}
	if record.Build.Status != "succeeded" {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:build:" + record.ServiceKey + ":BUILD_RECORD_NOT_ACCEPTED",
			Code:            deploymentv1.CodeBuildRecordNotAccepted,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         record.ServiceKey,
			Message:         "BuildRecord is not accepted for deployment (status: " + record.Build.Status + ")",
			RemediationCode: deploymentv1.RemediationCreateBuild,
		})
	}

	_, imgErr := deploymentv1.NewImmutableImage(record.Build.OCIRepository, record.Build.OCIDigest)
	if imgErr != nil {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:build:" + record.ServiceKey + ":BUILD_ARTIFACT_INVALID",
			Code:            deploymentv1.CodeBuildArtifactInvalid,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         record.ServiceKey,
			Message:         "BuildRecord image identity is invalid: " + imgErr.Error(),
			RemediationCode: deploymentv1.RemediationRebuildRequired,
		})
	} else if record.Build.Status == "succeeded" {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:           "chk:build:" + record.ServiceKey + ":BUILD_RECORD_VALID",
			Code:         "BUILD_RECORD_VALID",
			Severity:     deploymentv1.CheckSeverityPass,
			ScopeKind:    deploymentv1.ScopeKindApplication,
			ScopeID:      record.ServiceKey,
			Message:      "BuildRecord is accepted and immutable",
			SafeEvidence: map[string]string{"build_record_id": record.ID, "digest": record.Build.OCIDigest},
		})
	}

	// 2. Routing Decision & Deployment Policy
	decision, routeErr := s.Policies.Route(ctx, projectID, deploymentpolicyv1.RoutingRequest{
		BuildRecordID: record.ID,
		EnvironmentID: request.EnvironmentID,
	})
	if routeErr != nil {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:policy:" + record.ServiceKey + ":ROUTING_FAILED",
			Code:            deploymentv1.CodePlacementMissing,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindPolicy,
			ScopeID:         record.ServiceKey,
			Message:         routeErr.Error(),
			RemediationCode: deploymentv1.RemediationPlanPlacement,
		})
	} else if !decision.Eligible {
		decisionCode := decision.DecisionCode
		if decisionCode == "PLACEMENT_MISSING" || decisionCode == "TOPOLOGY_ASSIGNMENT_MISSING" {
			decisionCode = deploymentv1.CodePlacementMissing
		}
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:policy:" + record.ServiceKey + ":" + decisionCode,
			Code:            decisionCode,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindPolicy,
			ScopeID:         record.ServiceKey,
			Message:         decision.Message,
			RemediationCode: deploymentv1.RemediationPlanPlacement,
		})
	} else {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:           "chk:policy:" + record.ServiceKey + ":POLICY_ROUTING_VALID",
			Code:         "POLICY_ROUTING_VALID",
			Severity:     deploymentv1.CheckSeverityPass,
			ScopeKind:    deploymentv1.ScopeKindPolicy,
			ScopeID:      record.ServiceKey,
			Message:      "Deployment policy allows deployment to target runtime",
			SafeEvidence: map[string]string{"runtime_id": decision.RuntimeID, "policy_id": decision.DeploymentPolicyID},
		})
	}

	// 3. Topology Plan & Assignment
	plan, planErr := s.Topology.Get(ctx, projectID)
	if planErr != nil {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:topology:" + record.ServiceKey + ":TOPOLOGY_MISSING",
			Code:            deploymentv1.CodePlacementMissing,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindTopology,
			ScopeID:         record.ServiceKey,
			Message:         "TopologyPlan is missing",
			RemediationCode: deploymentv1.RemediationPlanPlacement,
		})
	} else {
		if decision.TopologyPlanID != "" && (plan.ID != decision.TopologyPlanID || plan.Revision != decision.TopologyRevision) {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:topology:" + record.ServiceKey + ":TOPOLOGY_CHANGED",
				Code:            deploymentv1.CodeTopologyReviewStale,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindTopology,
				ScopeID:         record.ServiceKey,
				Message:         "TopologyPlan changed during deployment resolution",
				RemediationCode: deploymentv1.RemediationPlanPlacement,
			})
		}
		if request.ExpectedTopologyRevision != 0 && (plan.Revision != request.ExpectedTopologyRevision || plan.PlanHash != request.ExpectedTopologyHash) {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:topology:" + record.ServiceKey + ":TOPOLOGY_REVIEW_STALE",
				Code:            deploymentv1.CodeTopologyReviewStale,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindTopology,
				ScopeID:         record.ServiceKey,
				Message:         "TopologyPlan changed after deployment review",
				RemediationCode: deploymentv1.RemediationPlanPlacement,
			})
		}

		assignment, hasAssignment := deploymentAssignment(plan.Assignments, record.ServiceKey, request.EnvironmentID)
		if !hasAssignment {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:placement:" + record.ServiceKey + ":PLACEMENT_MISSING",
				Code:            deploymentv1.CodePlacementMissing,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindTopology,
				ScopeID:         record.ServiceKey,
				Message:         "no applied topology assignment exists in the target environment",
				RemediationCode: deploymentv1.RemediationPlanPlacement,
			})
		} else if decision.RuntimeID != "" && assignment.RuntimeID != decision.RuntimeID {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:placement:" + record.ServiceKey + ":WORKLOAD_TOPOLOGY_MISMATCH",
				Code:            deploymentv1.CodePlacementMissing,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindTopology,
				ScopeID:         record.ServiceKey,
				Message:         "service assignment is unavailable in the active TopologyPlan",
				RemediationCode: deploymentv1.RemediationPlanPlacement,
			})
		} else {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:           "chk:placement:" + record.ServiceKey + ":PLACEMENT_VALID",
				Code:         "PLACEMENT_VALID",
				Severity:     deploymentv1.CheckSeverityPass,
				ScopeKind:    deploymentv1.ScopeKindTopology,
				ScopeID:      record.ServiceKey,
				Message:      "Applied topology assignment exists and matches target runtime",
				SafeEvidence: map[string]string{"runtime_id": assignment.RuntimeID, "replicas": strconv.Itoa(int(assignment.Replicas))},
			})
		}
	}

	// 4. Server / Runtime Node & Agent Live Health Check
	targetRuntimeID := decision.RuntimeID
	if targetRuntimeID == "" && planErr == nil {
		if assign, ok := deploymentAssignment(plan.Assignments, record.ServiceKey, request.EnvironmentID); ok {
			targetRuntimeID = assign.RuntimeID
		}
	}
	nodes, nodeErr := s.Registry.ListNodes(projectID)
	if nodeErr != nil || len(nodes) == 0 {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:runtime:" + targetRuntimeID + ":RUNTIME_NOT_FOUND",
			Code:            deploymentv1.CodeRuntimeNotFound,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindServer,
			ScopeID:         targetRuntimeID,
			Message:         "no nodes found for project",
			RemediationCode: deploymentv1.RemediationWaitForServer,
		})
	} else {
		var matchedNode *registry.Node
		for _, n := range nodes {
			if (targetRuntimeID != "" && n.RuntimeID == targetRuntimeID) || (decision.NodeID != "" && n.ID == decision.NodeID) {
				matchedNode = &n
				break
			}
		}
		if matchedNode == nil {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:runtime:" + targetRuntimeID + ":RUNTIME_NOT_FOUND",
				Code:            deploymentv1.CodeRuntimeNotFound,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindServer,
				ScopeID:         targetRuntimeID,
				Message:         "target runtime node not found",
				RemediationCode: deploymentv1.RemediationWaitForServer,
			})
		} else {
			if matchedNode.Status != registry.NodeHealthy {
				result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
					ID:              "chk:runtime:" + matchedNode.ID + ":RUNTIME_NOT_READY",
					Code:            deploymentv1.CodeRuntimeNotReady,
					Severity:        deploymentv1.CheckSeverityBlock,
					ScopeKind:       deploymentv1.ScopeKindServer,
					ScopeID:         matchedNode.ID,
					Message:         fmt.Sprintf("target server %s is not healthy (status: %s)", matchedNode.Name, matchedNode.Status),
					RemediationCode: deploymentv1.RemediationWaitForServer,
				})
			} else {
				result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
					ID:           "chk:runtime:" + matchedNode.ID + ":SERVER_HEALTHY",
					Code:         "SERVER_HEALTHY",
					Severity:     deploymentv1.CheckSeverityPass,
					ScopeKind:    deploymentv1.ScopeKindServer,
					ScopeID:      matchedNode.ID,
					Message:      fmt.Sprintf("server %s is healthy and online", matchedNode.Name),
					SafeEvidence: map[string]string{"node_id": matchedNode.ID, "status": matchedNode.Status},
				})
			}
		}
	}

	// 5. Service & ServiceConfiguration
	services, servErr := s.Registry.ListServices(projectID)
	if servErr != nil {
		services = []registry.ServiceRecord{}
	}
	var service registry.ServiceRecord
	for _, cand := range services {
		if cand.ID == record.ServiceID && cand.Name == record.ServiceKey {
			service = cand
			break
		}
	}
	if service.ID == "" {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:service:" + record.ServiceKey + ":SERVICE_NOT_FOUND",
			Code:            deploymentv1.CodeDependencyTargetMissing,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         record.ServiceKey,
			Message:         "service record for BuildRecord was not found",
			RemediationCode: deploymentv1.RemediationReviewConfiguration,
		})
		result.EvaluateStatus()
		result.SortChecks()
		return result, nil
	}

	configuration, cfgErr := s.Registry.GetServiceConfiguration(projectID, service.ID)
	if cfgErr != nil {
		result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
			ID:              "chk:config:" + service.Name + ":CONFIGURATION_MISSING",
			Code:            deploymentv1.CodeConfigurationReviewStale,
			Severity:        deploymentv1.CheckSeverityBlock,
			ScopeKind:       deploymentv1.ScopeKindApplication,
			ScopeID:         service.Name,
			Message:         "ServiceConfiguration missing",
			RemediationCode: deploymentv1.RemediationReviewConfiguration,
		})
	} else {
		if request.ExpectedConfigurationRevision != 0 && (configuration.Revision != request.ExpectedConfigurationRevision || configuration.StateHash != request.ExpectedConfigurationStateHash) {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:              "chk:config:" + service.Name + ":CONFIGURATION_REVIEW_STALE",
				Code:            deploymentv1.CodeConfigurationReviewStale,
				Severity:        deploymentv1.CheckSeverityBlock,
				ScopeKind:       deploymentv1.ScopeKindApplication,
				ScopeID:         service.Name,
				Message:         "ServiceConfiguration changed after deployment review",
				RemediationCode: deploymentv1.RemediationReviewConfiguration,
			})
		} else {
			result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
				ID:           "chk:config:" + service.Name + ":CONFIGURATION_VALID",
				Code:         "CONFIGURATION_VALID",
				Severity:     deploymentv1.CheckSeverityPass,
				ScopeKind:    deploymentv1.ScopeKindApplication,
				ScopeID:      service.Name,
				Message:      "ServiceConfiguration revision and state hash are valid",
				SafeEvidence: map[string]string{"revision": strconv.FormatUint(configuration.Revision, 10), "state_hash": configuration.StateHash},
			})
		}

		// Check build-time dependency freshness
		var hasBuildDep bool
		for _, dep := range configuration.Dependencies {
			if dep.InjectionPhase == "build" && dep.TargetKind == "application" {
				hasBuildDep = true
				break
			}
		}
		if hasBuildDep {
			currentBuildDepState := registry.ComputeBuildDependencyState(configuration, services)
			expectedHash := registry.ComputeBuildConfigHash(record.Workload.SHA, record.Build.BuildStrategy, service.Dockerfile, service.BuildContext, record.Build.OCIRepository, currentBuildDepState)
			if record.Build.ConfigHash != "" && record.Build.ConfigHash != expectedHash && (record.Build.BuildJobID != "" || record.Build.ConfigHash != strings.Repeat("a", 64)) {
				result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
					ID:              "chk:build:" + service.Name + ":BUILD_DEPENDENCY_STALE",
					Code:            deploymentv1.CodeBuildDependencyStale,
					Severity:        deploymentv1.CheckSeverityBlock,
					ScopeKind:       deploymentv1.ScopeKindApplication,
					ScopeID:         service.Name,
					Message:         "BuildRecord is stale because build-time dependency endpoints have changed; rebuild required",
					RemediationCode: deploymentv1.RemediationRebuildRequired,
				})
			}
		}

		// 6. Application Dependencies
		bindings, _ := s.Resources.ListBindings(ctx, projectID, request.EnvironmentID)
		deployments, _ := s.Registry.ListDeployments(projectID)

		runningServices := make(map[string]bool)
		for _, job := range deployments {
			if (job.RolloutState == deploymentv1.RolloutStateSucceeded || job.Status == deploymentv1.StateSucceeded) && job.EnvironmentID == request.EnvironmentID && job.ServiceID != "" {
				runningServices[job.ServiceID] = true
			}
		}

		batchSet := make(map[string]bool)
		batchSet[service.ID] = true
		batchSet[service.Name] = true
		for _, item := range request.DeploymentBatch {
			batchSet[item] = true
		}

		servicesByID := make(map[string]registry.ServiceRecord, len(services))
		servicesByName := make(map[string]registry.ServiceRecord, len(services))
		for _, s := range services {
			servicesByID[s.ID] = s
			servicesByName[s.Name] = s
		}

		for _, dep := range configuration.Dependencies {
			if dep.TargetKind == "managed_resource" {
				res, getErr := s.Resources.Get(ctx, projectID, dep.TargetIdentity)
				deleted := getErr != nil || res.EnvironmentID != request.EnvironmentID || res.Lifecycle == "deleted" || res.Lifecycle == "deleting" || res.Lifecycle == "failed" || res.Lifecycle == "retiring" || res.Lifecycle == "retired"
				if deleted {
					if dep.Required {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_REQUIRED_UNRESOLVED",
							Code:                  deploymentv1.CodeDependencyRequiredUnresolved,
							Severity:              deploymentv1.CheckSeverityBlock,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("required managed resource %s is unavailable or deleted", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationWaitForResource,
						})
					} else {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
							Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
							Severity:              deploymentv1.CheckSeverityWarn,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("optional managed resource %s is unavailable", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationWaitForResource,
						})
					}
					continue
				}

				// Resource exists. Check if Ready.
				isReady := res.Lifecycle == resourcev1.LifecycleReady
				if !isReady {
					if dep.Required {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_REQUIRED_UNRESOLVED",
							Code:                  deploymentv1.CodeDependencyRequiredUnresolved,
							Severity:              deploymentv1.CheckSeverityBlock,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("required managed resource %s (%s) is not Ready (lifecycle: %s)", dep.LogicalName, res.Name, res.Lifecycle),
							RemediationCode:       deploymentv1.RemediationWaitForResource,
						})
					} else {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
							Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
							Severity:              deploymentv1.CheckSeverityWarn,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("optional managed resource %s (%s) is not Ready (lifecycle: %s)", dep.LogicalName, res.Name, res.Lifecycle),
							RemediationCode:       deploymentv1.RemediationWaitForResource,
						})
					}
					continue
				}

				// Check ResourceBinding
				var matchingBinding *resourcev1.Binding
				var driftBinding *resourcev1.Binding
				for i := range bindings {
					b := &bindings[i]
					if b.Source.ID == service.ID && b.LogicalName == dep.LogicalName && b.Lifecycle != resourcev1.LifecycleDeleting {
						if b.Target.ID == dep.TargetIdentity {
							matchingBinding = b
						} else {
							driftBinding = b
						}
					}
				}

				if driftBinding != nil {
					result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
						ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_BINDING_STALE",
						Code:                  deploymentv1.CodeDependencyBindingStale,
						Severity:              deploymentv1.CheckSeverityBlock,
						ScopeKind:             deploymentv1.ScopeKindResource,
						ScopeID:               service.Name,
						DependencyLogicalName: dep.LogicalName,
						TargetSafeID:          dep.TargetIdentity,
						Message:               fmt.Sprintf("resource binding for %s points to %s but contract targets %s; explicit cutover migration required", dep.LogicalName, driftBinding.Target.ID, dep.TargetIdentity),
						RemediationCode:       deploymentv1.RemediationExplicitMigration,
					})
				} else if matchingBinding == nil {
					if dep.Required {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_REALIZATION_MISSING",
							Code:                  deploymentv1.CodeDependencyRealizationMissing,
							Severity:              deploymentv1.CheckSeverityBlock,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("required resource binding for %s is not realized", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationRealizeDependency,
						})
					} else {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
							Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
							Severity:              deploymentv1.CheckSeverityWarn,
							ScopeKind:             deploymentv1.ScopeKindResource,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("optional resource binding for %s is not realized", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationRealizeDependency,
						})
					}
				} else {
					result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
						ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_SATISFIED",
						Code:                  "DEPENDENCY_SATISFIED",
						Severity:              deploymentv1.CheckSeverityPass,
						ScopeKind:             deploymentv1.ScopeKindResource,
						ScopeID:               service.Name,
						DependencyLogicalName: dep.LogicalName,
						TargetSafeID:          dep.TargetIdentity,
						Message:               fmt.Sprintf("managed resource dependency %s is realized and ready", dep.LogicalName),
						SafeEvidence:          map[string]string{"logical_name": dep.LogicalName, "target_id": dep.TargetIdentity, "binding_id": matchingBinding.ID, "status": "ready"},
					})
				}
			} else if dep.TargetKind == "application" {
				targetApp, found := servicesByID[dep.TargetIdentity]
				if !found {
					targetApp, found = servicesByName[dep.TargetIdentity]
				}
				if !found || targetApp.Status == "deleted" || targetApp.ProjectID != projectID {
					if dep.Required {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_TARGET_MISSING",
							Code:                  deploymentv1.CodeDependencyTargetMissing,
							Severity:              deploymentv1.CheckSeverityBlock,
							ScopeKind:             deploymentv1.ScopeKindApplication,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("target application %s was not found or deleted", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationIncludeDependencyTarget,
						})
					} else {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
							Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
							Severity:              deploymentv1.CheckSeverityWarn,
							ScopeKind:             deploymentv1.ScopeKindApplication,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          dep.TargetIdentity,
							Message:               fmt.Sprintf("optional target application %s was not found", dep.LogicalName),
							RemediationCode:       deploymentv1.RemediationIncludeDependencyTarget,
						})
					}
					continue
				}

				targetRunning := runningServices[targetApp.ID] || runningServices[targetApp.Name]
				inBatch := batchSet[targetApp.ID] || batchSet[targetApp.Name]

				if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
					if !targetRunning && !inBatch {
						if dep.Required {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_INTERNAL_TARGET_UNAVAILABLE",
								Code:                  deploymentv1.CodeDependencyInternalTargetUnavailable,
								Severity:              deploymentv1.CheckSeverityBlock,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("target application %s is neither currently running nor included in the deployment batch", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationIncludeDependencyTarget,
							})
						} else {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
								Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
								Severity:              deploymentv1.CheckSeverityWarn,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("optional target application %s is not running or in batch", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationIncludeDependencyTarget,
							})
						}
					} else {
						targetAssign, hasTargetAssign := deploymentAssignment(plan.Assignments, targetApp.Name, request.EnvironmentID)
						if !hasTargetAssign || (targetRuntimeID != "" && targetAssign.RuntimeID != targetRuntimeID) {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":TARGET_ASSIGNMENT_MISSING",
								Code:                  deploymentv1.CodePlacementMissing,
								Severity:              deploymentv1.CheckSeverityBlock,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("target application %s must have an assignment on the same runtime", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationPlanPlacement,
							})
						} else if inBatch && !targetRunning {
							// Batch membership alone does NOT satisfy prerequisites.
							// The batch target must itself pass its own deployment prerequisites.
							// Gate 7: accepted BuildRecord must exist for the batch target.
							targetBuilds, _ := s.BuildRecords.List(ctx, projectID, buildrecord.ListFilter{ServiceKey: targetApp.Name, Status: "succeeded", Limit: 1})
							if len(targetBuilds.Records) == 0 {
								severity := deploymentv1.CheckSeverityBlock
								if !dep.Required {
									severity = deploymentv1.CheckSeverityWarn
								}
								result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
									ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":TARGET_BUILD_RECORD_MISSING",
									Code:                  deploymentv1.CodeBuildRecordMissing,
									Severity:              severity,
									ScopeKind:             deploymentv1.ScopeKindApplication,
									ScopeID:               service.Name,
									DependencyLogicalName: dep.LogicalName,
									TargetSafeID:          targetApp.ID,
									Message:               fmt.Sprintf("batch target application %s has no accepted BuildRecord; cannot satisfy deployment prerequisite", targetApp.Name),
									RemediationCode:       deploymentv1.RemediationCreateBuild,
								})
							} else {
								// Gate 9: target's required managed resource dependencies must be satisfied (transitive).
								targetCfgForDeps, cfgDepErr := s.Registry.GetServiceConfiguration(projectID, targetApp.ID)
								var foundBatchBlock bool
								if cfgDepErr == nil {
									for _, targetDep := range targetCfgForDeps.Dependencies {
										if targetDep.TargetKind != "managed_resource" || !targetDep.Required {
											continue
										}
										targetRes, resErr := s.Resources.Get(ctx, projectID, targetDep.TargetIdentity)
										targetResReady := resErr == nil &&
											targetRes.EnvironmentID == request.EnvironmentID &&
											targetRes.Lifecycle == resourcev1.LifecycleReady
										if !targetResReady {
											foundBatchBlock = true
											result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
												ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":TARGET_DEPENDENCY_UNRESOLVED",
												Code:                  deploymentv1.CodeDependencyRequiredUnresolved,
												Severity:              deploymentv1.CheckSeverityBlock,
												ScopeKind:             deploymentv1.ScopeKindApplication,
												ScopeID:               service.Name,
												DependencyLogicalName: dep.LogicalName,
												TargetSafeID:          targetApp.ID,
												Message:               fmt.Sprintf("batch target application %s has an unresolved required dependency %s; cannot be safely deployed", targetApp.Name, targetDep.LogicalName),
												RemediationCode:       deploymentv1.RemediationWaitForResource,
											})
										}
									}
								}
								if !foundBatchBlock {
									result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
										ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_SATISFIED",
										Code:                  "DEPENDENCY_SATISFIED",
										Severity:              deploymentv1.CheckSeverityPass,
										ScopeKind:             deploymentv1.ScopeKindApplication,
										ScopeID:               service.Name,
										DependencyLogicalName: dep.LogicalName,
										TargetSafeID:          targetApp.ID,
										Message:               fmt.Sprintf("internal HTTP target %s is available (batch member with accepted build and prerequisites)", targetApp.Name),
										SafeEvidence:          map[string]string{"strategy": "internal_http", "target": targetApp.Name, "source": "batch"},
									})
								}
							}
						} else {
							// targetRunning: already deployed and serving
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_SATISFIED",
								Code:                  "DEPENDENCY_SATISFIED",
								Severity:              deploymentv1.CheckSeverityPass,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("internal HTTP target %s is available (currently running)", targetApp.Name),
								SafeEvidence:          map[string]string{"strategy": "internal_http", "target": targetApp.Name, "source": "running"},
							})
						}
					}
				} else if dep.Strategy == serviceconfigurationv1.StrategyPublicHTTP {
					targetCfg, _ := s.Registry.GetServiceConfiguration(projectID, targetApp.ID)
					if targetCfg.PublicRoute == nil || targetCfg.PublicRoute.Hostname == "" {
						if dep.Required {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_PUBLIC_ENDPOINT_MISSING",
								Code:                  deploymentv1.CodeDependencyPublicEndpointMissing,
								Severity:              deploymentv1.CheckSeverityBlock,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("target application %s has no public route configured", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationConfigureExposure,
							})
						} else {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
								Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
								Severity:              deploymentv1.CheckSeverityWarn,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("optional target application %s has no public route configured", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationConfigureExposure,
							})
						}
					} else {
						result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
							ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_SATISFIED",
							Code:                  "DEPENDENCY_SATISFIED",
							Severity:              deploymentv1.CheckSeverityPass,
							ScopeKind:             deploymentv1.ScopeKindApplication,
							ScopeID:               service.Name,
							DependencyLogicalName: dep.LogicalName,
							TargetSafeID:          targetApp.ID,
							Message:               fmt.Sprintf("public HTTP target %s route is configured", targetApp.Name),
							SafeEvidence:          map[string]string{"strategy": "public_http", "target": targetApp.Name, "hostname": targetCfg.PublicRoute.Hostname, "path": targetCfg.PublicRoute.Path},
						})
					}
				} else if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
					targetCfg, _ := s.Registry.GetServiceConfiguration(projectID, targetApp.ID)
					if targetCfg.PublicRoute == nil || targetCfg.PublicRoute.Hostname == "" {
						if dep.Required {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_PUBLIC_ENDPOINT_MISSING",
								Code:                  deploymentv1.CodeDependencyPublicEndpointMissing,
								Severity:              deploymentv1.CheckSeverityBlock,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("same-origin target application %s has no public route configured", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationConfigureExposure,
							})
						} else {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_OPTIONAL_UNAVAILABLE",
								Code:                  deploymentv1.CodeDependencyOptionalUnavailable,
								Severity:              deploymentv1.CheckSeverityWarn,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("same-origin target application %s has no public route configured", targetApp.Name),
								RemediationCode:       deploymentv1.RemediationConfigureExposure,
							})
						}
					} else {
						expectedPath := dep.Path
						if expectedPath == "" {
							expectedPath = "/api"
						}
						if targetCfg.PublicRoute.Path != expectedPath {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":SAME_ORIGIN_PATH_MISMATCH",
								Code:                  deploymentv1.CodeDependencyRouteConflict,
								Severity:              deploymentv1.CheckSeverityBlock,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("same-origin dependency path %s does not match target route path %s", expectedPath, targetCfg.PublicRoute.Path),
								RemediationCode:       deploymentv1.RemediationConfigureExposure,
							})
						} else {
							result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
								ID:                    "chk:dep:" + service.Name + ":" + dep.LogicalName + ":DEPENDENCY_SATISFIED",
								Code:                  "DEPENDENCY_SATISFIED",
								Severity:              deploymentv1.CheckSeverityPass,
								ScopeKind:             deploymentv1.ScopeKindApplication,
								ScopeID:               service.Name,
								DependencyLogicalName: dep.LogicalName,
								TargetSafeID:          targetApp.ID,
								Message:               fmt.Sprintf("same-origin target %s route is configured", targetApp.Name),
								SafeEvidence:          map[string]string{"strategy": "same_origin", "target": targetApp.Name, "path": expectedPath},
							})
						}
					}
				}
			}
		}

		// 7. Route Conflicts
		if configuration.PublicRoute != nil {
			for _, other := range services {
				if other.ID == service.ID || other.Configuration.PublicRoute == nil {
					continue
				}
				otherRoute := other.Configuration.PublicRoute
				if otherRoute.Hostname == configuration.PublicRoute.Hostname && exposurev1.ManagedPathsConflict(otherRoute.Path, configuration.PublicRoute.Path) {
					result.Checks = append(result.Checks, deploymentv1.PreflightCheck{
						ID:              "chk:exposure:" + service.Name + ":ROUTE_CONFLICT",
						Code:            deploymentv1.CodeDependencyRouteConflict,
						Severity:        deploymentv1.CheckSeverityBlock,
						ScopeKind:       deploymentv1.ScopeKindApplication,
						ScopeID:         service.Name,
						Message:         "public route hostname and path conflict with an existing service",
						RemediationCode: deploymentv1.RemediationResolveRouteConflict,
					})
					break
				}
			}
		}
	}

	result.EvaluateStatus()
	result.SortChecks()

	revisions := map[string]string{
		"environment_id": request.EnvironmentID,
	}
	if planErr == nil {
		revisions["topology_revision"] = strconv.FormatUint(plan.Revision, 10)
		revisions["topology_hash"] = plan.PlanHash
	}
	if cfgErr == nil {
		revisions["configuration_revision"] = strconv.FormatUint(configuration.Revision, 10)
		revisions["configuration_state_hash"] = configuration.StateHash
	}
	if routeErr == nil {
		revisions["policy_revision"] = strconv.FormatUint(decision.DeploymentPolicyRevision, 10)
		if policy, err := s.Policies.Get(ctx, projectID, decision.DeploymentPolicyID); err == nil { revisions["policy_hash"] = policy.PolicyHash }
		revisions["routing_decision_hash"] = decision.DecisionHash
	}
	batchList := append([]string{service.Name}, request.DeploymentBatch...)
	result.PreflightHash = result.ComputeHash(batchList, revisions)
	result.AuthorityFingerprint = result.PreflightHash

	return result, nil
}
