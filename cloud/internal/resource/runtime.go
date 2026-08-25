package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

const managedLeaseTTL = 2 * time.Minute

type RuntimeTargetResolver interface {
	ResolveManagedResourceTarget(context.Context, string, string, string) (resourcev1.ManagedResourceAssignment, error)
}

type ManagedLease struct {
	Spec       resourcev1.ManagedResourceSpec        `json:"spec"`
	Action     string                                `json:"action"`
	LeaseToken string                                `json:"lease_token"`
	Credential *resourcev1.ManagedResourceCredential `json:"credential,omitempty"`
	Bindings   []resourcev1.PostgresBindingOperation `json:"bindings,omitempty"`
}

type ManagedResult struct {
	Status         string                              `json:"status"`
	LeaseToken     string                              `json:"lease_token"`
	Evidence       *resourcev1.ManagedResourceEvidence `json:"evidence,omitempty"`
	FailureCode    string                              `json:"failure_code,omitempty"`
	FailureMessage string                              `json:"failure_message_redacted,omitempty"`
	BindingResults []resourcev1.PostgresBindingResult  `json:"binding_results,omitempty"`
}

func (s Service) ValidateTopologyProvisioning(ctx context.Context, projectID string, draft topologyv1.Draft) error {
	resources, err := s.List(ctx, projectID, "")
	if err != nil {
		return err
	}
	byID := make(map[string]resourcev1.Resource, len(resources))
	for _, value := range resources {
		byID[value.ID] = value
	}
	for _, assignment := range draft.Assignments {
		value, ok := byID[assignment.ServiceKey]
		if !ok || value.Kind != resourcev1.KindManagedService {
			continue
		}
		definition, known := resourcev1.Definition(value.Type)
		if !known || !definition.Provisioning.Implemented {
			return invalid("MANAGED_RESOURCE_PROVISIONING_UNSUPPORTED", "managed resource provisioning is not implemented for this type")
		}
		if value.Runtime != nil && value.Runtime.Spec.Assignment.RuntimeID != assignment.RuntimeID {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "managed resource placement moves are unsupported in P07B1")
		}
		if value.Managed == nil || assignment.Replicas != value.Managed.Replicas || assignment.CPURequestMillicores != value.Managed.CPUMillicores || assignment.MemoryRequestBytes != value.Managed.MemoryBytes {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "managed resource placement resources must match the canonical Resource spec")
		}
	}
	assigned := make(map[string]bool, len(draft.Assignments))
	for _, assignment := range draft.Assignments {
		assigned[assignment.ServiceKey] = true
	}
	for _, value := range resources {
		if value.Kind == resourcev1.KindManagedService && value.Runtime != nil && !assigned[value.ID] {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "removing placement from a provisioned managed resource is unsupported in P07B1")
		}
	}
	return nil
}

func (s Service) ReconcileTopology(ctx context.Context, projectID string, plan topologyv1.Plan, targets RuntimeTargetResolver) error {
	if targets == nil {
		return unavailable()
	}
	resources, err := s.List(ctx, projectID, "")
	if err != nil {
		return err
	}
	assignments := make(map[string]topologyv1.Assignment, len(plan.Assignments))
	for _, assignment := range plan.Assignments {
		assignments[assignment.ServiceKey] = assignment
	}
	for _, value := range resources {
		if value.Kind != resourcev1.KindManagedService || value.Lifecycle == resourcev1.LifecycleDeleting {
			continue
		}
		assignment, ok := assignments[value.ID]
		if !ok {
			if value.Runtime != nil {
				return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "removing placement from a provisioned managed resource is unsupported in P07B1")
			}
			continue
		}
		if value.Runtime != nil && value.Runtime.Spec.Assignment.RuntimeID != assignment.RuntimeID {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "managed resource placement moves are unsupported in P07B1")
		}
		if value.Managed == nil || assignment.Replicas != value.Managed.Replicas || assignment.CPURequestMillicores != value.Managed.CPUMillicores || assignment.MemoryRequestBytes != value.Managed.MemoryBytes {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "managed resource placement resources must match the canonical Resource spec")
		}
		target, err := targets.ResolveManagedResourceTarget(ctx, projectID, value.EnvironmentID, assignment.RuntimeID)
		if err != nil {
			return invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "managed resource assignment has no unique factual Agent target")
		}
		credentialID := ""
		if managedCredentialRequired(value.Type) {
			if s.Credentials == nil {
				return invalid(resourcev1.FailureCredentialUnavailable, "managed resource credential authority is unavailable")
			}
			credentialID = managedCredentialID(value.ID)
			if _, err := s.Credentials.Ensure(ctx, credentialID); err != nil {
				return invalid(resourcev1.FailureCredentialUnavailable, "managed resource credential could not be generated")
			}
		}
		spec, err := compileManaged(value, target, plan.Revision, plan.PlanHash, credentialID)
		if err != nil {
			return err
		}
		if value.Runtime != nil && value.Runtime.Spec.SpecHash == spec.SpecHash {
			continue
		}
		value.Runtime = &resourcev1.ManagedResourceRuntime{Spec: spec}
		value.Lifecycle = resourcev1.LifecyclePlanned
		value.UpdatedAt = s.clock()
		if _, err := s.Store.Update(ctx, value); err != nil {
			return err
		}
	}
	return nil
}

func compileManaged(value resourcev1.Resource, assignment resourcev1.ManagedResourceAssignment, topologyRevision uint64, topologyHash, credentialID string) (resourcev1.ManagedResourceSpec, error) {
	if value.Managed == nil {
		return resourcev1.ManagedResourceSpec{}, invalid("MANAGED_RESOURCE_SPEC_INVALID", "managed resource spec is missing")
	}
	definition, ok := resourcev1.Definition(value.Type)
	if !ok || !definition.Provisioning.Implemented {
		return resourcev1.ManagedResourceSpec{}, invalid("MANAGED_RESOURCE_PROVISIONING_UNSUPPORTED", "managed resource provisioning is not implemented for this type")
	}
	if assignment.RuntimeID == "" || assignment.NodeID == "" || assignment.AgentID == "" {
		return resourcev1.ManagedResourceSpec{}, invalid("MANAGED_RESOURCE_ASSIGNMENT_INVALID", "runtime, node, and Agent assignment are required")
	}
	profile := defaultString(value.Managed.Profile, definition.Provisioning.Profiles[0].Name)
	version := defaultString(value.Managed.Version, definition.Provisioning.Profiles[0].Versions[0].Version)
	if version == "default" {
		version = definition.Provisioning.Profiles[0].Versions[0].Version
	}
	var image string
	for _, supportedProfile := range definition.Provisioning.Profiles {
		if supportedProfile.Name != profile {
			continue
		}
		for _, supported := range supportedProfile.Versions {
			if supported.Version == version {
				image = supported.Image
			}
		}
	}
	if image == "" {
		return resourcev1.ManagedResourceSpec{}, invalid("MANAGED_RESOURCE_IMAGE_UNAVAILABLE", "managed resource version/profile does not resolve to a trusted image")
	}
	configurationHash := hashValue(value.Managed.ServiceConfig)
	// StatefulSet controller-revision labels append their own hash to the
	// workload name. Keep managed resource names compact so those labels stay
	// within Kubernetes' 63-character limit.
	serviceName := deploymentv1.StableDNSName("omr", value.ID)
	host := internalHost(value.ProjectID, value.EnvironmentID, serviceName)
	portName, protocol, database, connectionURL := "nats", resourcev1.ProtocolNATS, "", "nats://"+host+":"+strconv.Itoa(definition.DefaultPort)
	if value.Type == resourcev1.TypeRedis {
		portName, protocol, connectionURL = "redis", resourcev1.ProtocolRedis, ""
		if credentialID == "" {
			return resourcev1.ManagedResourceSpec{}, invalid(resourcev1.FailureCredentialUnavailable, "managed Redis credential identity is unavailable")
		}
	} else if value.Type == resourcev1.TypePostgres {
		portName, protocol, database, connectionURL = "postgres", resourcev1.ProtocolPostgres, "opsi", ""
		if credentialID == "" {
			return resourcev1.ManagedResourceSpec{}, invalid(resourcev1.FailureCredentialUnavailable, "managed PostgreSQL credential identity is unavailable")
		}
	}
	storage := value.Managed.Storage
	if value.Type == resourcev1.TypePostgres && storage.PolicyRef == "" {
		storage.PolicyRef = resourcev1.StoragePolicyDefault
	}
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: value.ID, ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID,
		ResourceType: value.Type, Profile: profile, Version: version, Image: image, Assignment: assignment,
		Replicas: value.Managed.Replicas, CPUMillicores: value.Managed.CPUMillicores, MemoryBytes: value.Managed.MemoryBytes,
		Ports: []resourcev1.ManagedResourcePort{{Name: portName, Port: int32(definition.DefaultPort), Protocol: protocol}}, Storage: storage,
		Connection:        resourcev1.ManagedResourceConnection{ServiceName: serviceName, Host: host, Port: int32(definition.DefaultPort), Protocol: protocol, Database: database, URL: connectionURL},
		CredentialID:      credentialID,
		ConfigurationHash: configurationHash, TopologyRevision: topologyRevision, TopologyHash: topologyHash,
	}
	hash, err := spec.Hash()
	if err != nil {
		return resourcev1.ManagedResourceSpec{}, invalid("MANAGED_RESOURCE_SPEC_INVALID", "managed resource spec could not be hashed")
	}
	spec.SpecHash = hash
	return spec, nil
}

func (s Service) LeaseManaged(ctx context.Context, projectID, nodeID string) (ManagedLease, bool, error) {
	now := s.clock()
	token := newID("mrlease")
	value, ok, err := s.Store.ClaimManaged(ctx, projectID, nodeID, token, now, now.Add(managedLeaseTTL))
	if err != nil || !ok {
		return ManagedLease{}, false, err
	}
	action := "apply"
	if value.Lifecycle == resourcev1.LifecycleDeleting {
		action = "delete"
	}
	lease := ManagedLease{Spec: value.Runtime.Spec, Action: action, LeaseToken: token}
	if action == "apply" && managedCredentialRequired(value.Type) {
		if s.Credentials == nil {
			return ManagedLease{}, false, invalid(resourcev1.FailureCredentialUnavailable, "managed resource credential authority is unavailable")
		}
		credential, err := s.Credentials.Get(ctx, value.Runtime.Spec.CredentialID)
		if err != nil {
			return ManagedLease{}, false, invalid(resourcev1.FailureCredentialUnavailable, "managed resource credential is unavailable")
		}
		lease.Credential = &credential
	}
	if action == "apply" && value.Type == resourcev1.TypePostgres {
		bindings, err := s.postgresBindingOperations(ctx, value)
		if err != nil {
			return ManagedLease{}, false, err
		}
		lease.Bindings = bindings
	}
	return lease, true, nil
}

func (s Service) postgresBindingOperations(ctx context.Context, target resourcev1.Resource) ([]resourcev1.PostgresBindingOperation, error) {
	bindings, err := s.ListBindings(ctx, target.ProjectID, target.EnvironmentID)
	if err != nil {
		return nil, err
	}
	operations := []resourcev1.PostgresBindingOperation{}
	for _, binding := range bindings {
		if binding.Target.ID != target.ID || binding.CredentialID == "" {
			continue
		}
		operation := resourcev1.PostgresBindingOperation{BindingID: binding.ID, CredentialID: binding.CredentialID, RoleName: binding.RoleName, Database: binding.Database, Action: resourcev1.PostgresBindingEnsure, Create: binding.Lifecycle == resourcev1.LifecycleProvisioning}
		if binding.Lifecycle == resourcev1.LifecycleDeleting {
			operation.Action = resourcev1.PostgresBindingRevoke
		} else {
			credential, getErr := s.Credentials.Get(ctx, binding.CredentialID)
			if getErr != nil || credential.ValidateBinding(binding.ID, target.ID) != nil {
				return nil, invalid(resourcev1.FailureBindingCredentialUnavailable, "PostgreSQL binding credential is unavailable")
			}
			operation.Credential = &credential
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (s Service) CompleteManaged(ctx context.Context, projectID, resourceID string, result ManagedResult) (resourcev1.Resource, error) {
	value, err := s.Get(ctx, projectID, resourceID)
	if err != nil {
		return resourcev1.Resource{}, err
	}
	if value.Runtime == nil || value.Runtime.LeaseToken == "" || result.LeaseToken != value.Runtime.LeaseToken {
		return resourcev1.Resource{}, invalid("MANAGED_RESOURCE_APPLY_FAILED", "managed resource lease is invalid")
	}
	value.Runtime.FailureCode = strings.TrimSpace(result.FailureCode)
	value.Runtime.FailureMessage = strings.TrimSpace(result.FailureMessage)
	value.Runtime.Evidence = result.Evidence
	if result.Status == "deleted" && result.Evidence != nil && result.Evidence.Deleted {
		if value.Runtime.Spec.CredentialID != "" && (s.Credentials == nil || s.Credentials.Delete(ctx, value.Runtime.Spec.CredentialID) != nil) {
			return resourcev1.Resource{}, invalid(resourcev1.FailureCredentialUnavailable, "managed resource credential could not be deleted")
		}
		if value.Type == resourcev1.TypePostgres {
			retained, retainedErr := retainedStorageFromDeletion(value, result.Evidence, s.clock())
			if retainedErr != nil {
				return resourcev1.Resource{}, retainedErr
			}
			if err := s.Store.RetainAndDeleteClaimed(ctx, value, retained, result.LeaseToken); err != nil {
				return resourcev1.Resource{}, err
			}
			return resourcev1.Resource{}, nil
		}
		if err := s.Store.DeleteClaimed(ctx, projectID, resourceID, result.LeaseToken); err != nil {
			return resourcev1.Resource{}, err
		}
		return resourcev1.Resource{}, nil
	}
	if result.Status == "ready" && factualReady(value.Runtime.Spec, result.Evidence) {
		value.Lifecycle = resourcev1.LifecycleReady
	} else if result.Evidence != nil && result.Evidence.Image != "" && result.Evidence.Image != value.Runtime.Spec.Image {
		value.Lifecycle = resourcev1.LifecycleDegraded
		value.Runtime.FailureCode = resourcev1.FailureRuntimeMismatch
	} else if result.Status == "failed" {
		value.Lifecycle = resourcev1.LifecycleFailed
	} else {
		value.Lifecycle = resourcev1.LifecycleUnknown
	}
	if retry, bindingErr := s.completePostgresBindings(ctx, value, result.BindingResults); bindingErr != nil {
		return resourcev1.Resource{}, bindingErr
	} else if retry {
		value.Lifecycle = resourcev1.LifecyclePlanned
	}
	value.UpdatedAt = s.clock()
	return s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
}

func (s Service) completePostgresBindings(ctx context.Context, target resourcev1.Resource, results []resourcev1.PostgresBindingResult) (bool, error) {
	if target.Type != resourcev1.TypePostgres {
		return false, nil
	}
	byID := make(map[string]resourcev1.PostgresBindingResult, len(results))
	for _, result := range results {
		byID[result.BindingID] = result
	}
	bindings, err := s.ListBindings(ctx, target.ProjectID, target.EnvironmentID)
	if err != nil {
		return false, err
	}
	retry := false
	for _, binding := range bindings {
		if binding.Target.ID != target.ID || binding.CredentialID == "" {
			continue
		}
		result, ok := byID[binding.ID]
		if !ok {
			if binding.Lifecycle != resourcev1.LifecycleReady {
				retry = true
			}
			continue
		}
		if result.Status != "ready" {
			binding.FailureCode = result.FailureCode
			if binding.Lifecycle != resourcev1.LifecycleDeleting {
				binding.Lifecycle = resourcev1.LifecycleFailed
			}
			binding.UpdatedAt = s.clock()
			if _, err := s.Store.UpdateBinding(ctx, binding); err != nil {
				return false, err
			}
			retry = true
			continue
		}
		if result.Action == resourcev1.PostgresBindingRevoke {
			if err := s.Credentials.Delete(ctx, binding.CredentialID); err != nil {
				return false, invalid(resourcev1.FailureBindingCredentialUnavailable, "PostgreSQL binding credential could not be removed")
			}
			if err := s.Store.DeleteBinding(ctx, target.ProjectID, binding.ID); err != nil {
				return false, err
			}
			continue
		}
		binding.Lifecycle, binding.FailureCode, binding.UpdatedAt = resourcev1.LifecycleReady, "", s.clock()
		if _, err := s.Store.UpdateBinding(ctx, binding); err != nil {
			return false, err
		}
	}
	return retry, nil
}

func factualReady(spec resourcev1.ManagedResourceSpec, evidence *resourcev1.ManagedResourceEvidence) bool {
	return evidence != nil && evidence.ObservedSpecHash == spec.SpecHash && evidence.WorkloadReady && evidence.PodReady && evidence.ServiceReady && (!managedCredentialRequired(spec.ResourceType) || evidence.SecretReady && evidence.AuthReady) && (spec.ResourceType != resourcev1.TypePostgres || evidence.StorageReady && evidence.VolumeMounted && evidence.PVCName != "" && evidence.PVName != "") && evidence.Image == spec.Image && imageIDMatches(evidence.ImageID, spec.Image) && evidence.AvailableReplicas >= spec.Replicas
}

func managedCredentialRequired(resourceType resourcev1.Type) bool {
	return resourceType == resourcev1.TypeRedis || resourceType == resourcev1.TypePostgres
}

func imageIDMatches(imageID, reference string) bool {
	parts := strings.Split(reference, "@")
	return imageID != "" && len(parts) == 2 && (strings.HasSuffix(imageID, "@"+parts[1]) || strings.HasSuffix(imageID, "://"+parts[1]) || imageID == parts[1])
}

func managedCredentialID(resourceID string) string { return "mrcred-" + resourceID }

func runtimeRefs(target resourcev1.Resource) []resourcev1.RuntimeConnectionReference {
	if target.Kind == resourcev1.KindManagedService {
		if target.Lifecycle != resourcev1.LifecycleReady || target.Runtime == nil || !factualReady(target.Runtime.Spec, target.Runtime.Evidence) {
			return nil
		}
		refs := []resourcev1.RuntimeConnectionReference{
			{Name: "HOST", Sensitivity: resourcev1.ValueNonSecret, Value: target.Runtime.Spec.Connection.Host},
			{Name: "PORT", Sensitivity: resourcev1.ValueNonSecret, Value: strconv.Itoa(int(target.Runtime.Spec.Connection.Port))},
		}
		if target.Type == resourcev1.TypeRedis {
			for _, name := range []string{"USER", "PASSWORD", "URL"} {
				refs = append(refs, resourcev1.RuntimeConnectionReference{Name: name, Sensitivity: resourcev1.ValueSecret, SecretRef: &resourcev1.SecretReference{SecretID: target.Runtime.Spec.CredentialID}})
			}
		} else {
			refs = append(refs, resourcev1.RuntimeConnectionReference{Name: "URL", Sensitivity: resourcev1.ValueNonSecret, Value: target.Runtime.Spec.Connection.URL})
		}
		return refs
	}
	return legacyRuntimeRefs(target)
}

func internalHost(projectID, environmentID, serviceName string) string {
	namespace := deploymentv1.StableDNSName("opsi", projectID, environmentID)
	return serviceName + "." + namespace + ".svc.cluster.local"
}

func hashValue(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func provisioningError(code string, err error) error {
	return invalid(code, fmt.Sprintf("managed resource provisioning failed: %v", err))
}
