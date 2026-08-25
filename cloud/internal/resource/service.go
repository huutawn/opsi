// Package resource owns managed and external infrastructure resource authority.
package resource

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	maxReplicas      = 20
	maxCPUMillicores = int64(1_000_000)
	maxMemoryBytes   = int64(1 << 50)
)

var ErrNotFound = errors.New("resource not found")

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Code + ": " + e.Message }

type ScopeAuthority interface {
	EnvironmentExists(context.Context, string, string) (bool, error)
	RuntimeBelongs(context.Context, string, string, string) (bool, error)
	ApplicationBelongs(context.Context, string, string, string) (bool, error)
}

type Store interface {
	Create(context.Context, resourcev1.Resource, string, string) (resourcev1.Resource, bool, error)
	Get(context.Context, string, string) (resourcev1.Resource, error)
	List(context.Context, string, string) ([]resourcev1.Resource, error)
	Update(context.Context, resourcev1.Resource) (resourcev1.Resource, error)
	ClaimManaged(context.Context, string, string, string, time.Time, time.Time) (resourcev1.Resource, bool, error)
	UpdateClaimed(context.Context, resourcev1.Resource, string) (resourcev1.Resource, error)
	Delete(context.Context, string, string) error
	DeleteClaimed(context.Context, string, string, string) error
	RetainAndDeleteClaimed(context.Context, resourcev1.Resource, resourcev1.RetainedStorage, string) error
	GetRetainedStorage(context.Context, string, string) (resourcev1.RetainedStorage, error)
	GetRetainedStorageByResource(context.Context, string, string) (resourcev1.RetainedStorage, error)
	ListRetainedStorage(context.Context, string, string) ([]resourcev1.RetainedStorage, error)
	SaveRetainedStorageReview(context.Context, string, string, uint64, string, string, time.Time) (resourcev1.RetainedStorage, bool, bool, error)
	RequestRetainedStorageDestroy(context.Context, string, string, string, string, string, string, time.Time) (resourcev1.RetainedStorage, bool, error)
	ClaimRetainedStorageDestroy(context.Context, string, string, string, time.Time, time.Time) (resourcev1.RetainedStorage, bool, error)
	UpdateRetainedStorageClaimed(context.Context, resourcev1.RetainedStorage, string) (resourcev1.RetainedStorage, error)
	CreateBinding(context.Context, resourcev1.Binding, string, string) (resourcev1.Binding, bool, error)
	GetBinding(context.Context, string, string) (resourcev1.Binding, error)
	ListBindings(context.Context, string, string) ([]resourcev1.Binding, error)
	UpdateBinding(context.Context, resourcev1.Binding) (resourcev1.Binding, error)
	DeleteBinding(context.Context, string, string) error
}

type CredentialAuthority interface {
	Ensure(context.Context, string) (resourcev1.ManagedResourceCredential, error)
	EnsureBinding(context.Context, resourcev1.BindingCredentialSpec) (resourcev1.ManagedResourceCredential, error)
	EnsureWorkloadSecret(context.Context, resourcev1.WorkloadSecretSpec) (resourcev1.ManagedResourceCredential, error)
	ListWorkloadSecrets(context.Context, string, string) ([]resourcev1.WorkloadSecretMetadata, error)
	GetWorkloadSecret(context.Context, string, string, string) (resourcev1.WorkloadSecretMetadata, error)
	UpsertWorkloadSecret(context.Context, resourcev1.WorkloadSecretUpsert) (resourcev1.WorkloadSecretMetadata, bool, error)
	BindWorkloadSecret(context.Context, string, string, string, string) (resourcev1.WorkloadSecretMetadata, error)
	Get(context.Context, string) (resourcev1.ManagedResourceCredential, error)
	Delete(context.Context, string) error
}

type ActiveOperationAuthority interface {
	HasActive(context.Context, string, string) (bool, error)
}

type Service struct {
	Store       Store
	Scopes      ScopeAuthority
	Credentials CredentialAuthority
	Operations  []ActiveOperationAuthority
	Now         func() time.Time
}

func (s Service) Definitions() []resourcev1.ResourceTypeDefinition { return resourcev1.Definitions() }

func (s Service) Create(ctx context.Context, projectID, actor, key string, request resourcev1.CreateRequest) (resourcev1.Resource, bool, error) {
	if s.Store == nil || s.Scopes == nil {
		return resourcev1.Resource{}, false, unavailable()
	}
	if !validKey(key) {
		return resourcev1.Resource{}, false, invalid("RESOURCE_IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	}
	exists, err := s.Scopes.EnvironmentExists(ctx, projectID, request.EnvironmentID)
	if err != nil {
		return resourcev1.Resource{}, false, err
	}
	if !exists {
		return resourcev1.Resource{}, false, Error{Code: "RESOURCE_ENVIRONMENT_NOT_FOUND", Status: 404, Message: "environment is not available in this project"}
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 128 {
		return resourcev1.Resource{}, false, invalid("RESOURCE_NAME_INVALID", "resource name is required and must not exceed 128 characters")
	}
	if err := validateCreate(request); err != nil {
		return resourcev1.Resource{}, false, err
	}
	now := s.clock()
	value := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion, ID: newID("res"), ProjectID: projectID, EnvironmentID: request.EnvironmentID,
		Name: request.Name, Kind: request.Kind, Provider: strings.TrimSpace(request.Provider), Type: request.Type, Managed: cloneManaged(request.Managed), External: cloneExternal(request.External),
		CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if value.Kind == resourcev1.KindManagedService {
		value.Provider = "opsi"
		value.Lifecycle = resourcev1.LifecycleUnplaced
		value.InternalName = internalHost(value.ProjectID, value.EnvironmentID, value.ID)
	} else {
		value.Lifecycle = resourcev1.LifecycleConfigured
	}
	payload := createPayload(request)
	return s.Store.Create(ctx, value, key, payload)
}

func (s Service) Get(ctx context.Context, projectID, resourceID string) (resourcev1.Resource, error) {
	if s.Store == nil {
		return resourcev1.Resource{}, unavailable()
	}
	return s.Store.Get(ctx, projectID, resourceID)
}

func (s Service) List(ctx context.Context, projectID, environmentID string) ([]resourcev1.Resource, error) {
	if s.Store == nil {
		return nil, unavailable()
	}
	return s.Store.List(ctx, projectID, environmentID)
}

func (s Service) Update(ctx context.Context, projectID, resourceID string, request resourcev1.UpdateRequest) (resourcev1.Resource, error) {
	current, err := s.Get(ctx, projectID, resourceID)
	if err != nil {
		return resourcev1.Resource{}, err
	}
	switch current.Kind {
	case resourcev1.KindManagedService:
		if current.Type == resourcev1.TypePostgres {
			active, err := s.hasActiveOperation(ctx, projectID, resourceID)
			if err != nil {
				return resourcev1.Resource{}, err
			}
			if active {
				return resourcev1.Resource{}, Error{Code: "RESOURCE_ACTIVE_OPERATION_CONFLICT", Status: 409, Message: "resource has an active backup"}
			}
		}
		if request.Managed == nil || request.External != nil || request.Managed.Type != current.Type {
			return resourcev1.Resource{}, invalid("RESOURCE_SPEC_INVALID", "managed resource update must preserve kind and type")
		}
		if err := validateManaged(*request.Managed); err != nil {
			return resourcev1.Resource{}, err
		}
		if err := validateManagedUpdate(current, *request.Managed); err != nil {
			return resourcev1.Resource{}, err
		}
		current.Managed = cloneManaged(request.Managed)
		if current.Runtime != nil {
			spec, compileErr := compileManaged(current, current.Runtime.Spec.Assignment, current.Runtime.Spec.TopologyRevision, current.Runtime.Spec.TopologyHash, current.Runtime.Spec.CredentialID)
			if compileErr != nil {
				return resourcev1.Resource{}, compileErr
			}
			current.Runtime = &resourcev1.ManagedResourceRuntime{Spec: spec}
			current.Lifecycle = resourcev1.LifecyclePlanned
		} else {
			current.Lifecycle = resourcev1.LifecycleUnplaced
		}
	case resourcev1.KindExternalResource:
		if request.External == nil || request.Managed != nil {
			return resourcev1.Resource{}, invalid("RESOURCE_SPEC_INVALID", "external resource update must preserve kind")
		}
		if err := validateExternal(*request.External); err != nil {
			return resourcev1.Resource{}, err
		}
		current.External = cloneExternal(request.External)
		current.Lifecycle = resourcev1.LifecycleConfigured
	default:
		return resourcev1.Resource{}, invalid("RESOURCE_KIND_INVALID", "resource kind is unsupported")
	}
	current.UpdatedAt = s.clock()
	return s.Store.Update(ctx, current)
}

func (s Service) DeleteIntent(ctx context.Context, projectID, resourceID, actor string) (resourcev1.Resource, error) {
	current, err := s.Get(ctx, projectID, resourceID)
	if err != nil {
		return resourcev1.Resource{}, err
	}
	if current.Type == resourcev1.TypePostgres {
		active, err := s.hasActiveOperation(ctx, projectID, resourceID)
		if err != nil {
			return resourcev1.Resource{}, err
		}
		if active {
			return resourcev1.Resource{}, Error{Code: "RESOURCE_ACTIVE_OPERATION_CONFLICT", Status: 409, Message: "resource has an active backup"}
		}
		bindings, err := s.ListBindings(ctx, projectID, current.EnvironmentID)
		if err != nil {
			return resourcev1.Resource{}, err
		}
		for _, binding := range bindings {
			if binding.Target.ID == resourceID {
				return resourcev1.Resource{}, Error{Code: resourcev1.FailureBindingActive, Status: 409, Message: "resource has active bindings"}
			}
		}
	}
	if current.Runtime == nil {
		if err := s.Store.Delete(ctx, projectID, resourceID); err != nil {
			return resourcev1.Resource{}, err
		}
		current.Lifecycle = resourcev1.LifecycleDeleting
		current.UpdatedAt = s.clock()
		return current, nil
	}
	current.Lifecycle = resourcev1.LifecycleDeleting
	current.Runtime.DeleteActor = actor
	current.UpdatedAt = s.clock()
	return s.Store.Update(ctx, current)
}

func (s Service) hasActiveOperation(ctx context.Context, projectID, resourceID string) (bool, error) {
	for _, authority := range s.Operations {
		if authority == nil {
			continue
		}
		active, err := authority.HasActive(ctx, projectID, resourceID)
		if err != nil || active {
			return active, err
		}
	}
	return false, nil
}

func (s Service) CreateBinding(ctx context.Context, projectID, key string, request resourcev1.CreateBindingRequest) (resourcev1.Binding, bool, error) {
	if s.Store == nil || s.Scopes == nil {
		return resourcev1.Binding{}, false, unavailable()
	}
	if !validKey(key) {
		return resourcev1.Binding{}, false, invalid("RESOURCE_IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	}
	if request.Source.Kind != resourcev1.KindApplication || request.Target.Kind == resourcev1.KindApplication || request.Source.ID == "" || request.Target.ID == "" {
		return resourcev1.Binding{}, false, invalid("RESOURCE_BINDING_ENDPOINT_INVALID", "binding must connect an application to a resource")
	}
	applicationOK, err := s.Scopes.ApplicationBelongs(ctx, projectID, request.EnvironmentID, request.Source.ID)
	if err != nil {
		return resourcev1.Binding{}, false, err
	}
	if !applicationOK {
		return resourcev1.Binding{}, false, Error{Code: "RESOURCE_BINDING_SOURCE_NOT_FOUND", Status: 404, Message: "application is not available in this environment"}
	}
	target, err := s.Get(ctx, projectID, request.Target.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return resourcev1.Binding{}, false, err
	}
	if err != nil || target.EnvironmentID != request.EnvironmentID || target.Kind != request.Target.Kind {
		return resourcev1.Binding{}, false, Error{Code: "RESOURCE_BINDING_TARGET_NOT_FOUND", Status: 404, Message: "resource is not available in this environment"}
	}
	if !protocolCompatible(target, request.Protocol) {
		return resourcev1.Binding{}, false, invalid("RESOURCE_PROTOCOL_UNSUPPORTED", "resource does not support the requested protocol")
	}
	logical := strings.TrimSpace(request.LogicalName)
	if logical == "" || len(logical) > 64 {
		return resourcev1.Binding{}, false, invalid("RESOURCE_BINDING_NAME_INVALID", "binding logical name is required")
	}
	if target.Kind == resourcev1.KindManagedService && target.Type == resourcev1.TypePostgres && (target.Lifecycle == resourcev1.LifecycleDeleting || target.Runtime == nil || !factualReady(target.Runtime.Spec, target.Runtime.Evidence)) {
		return resourcev1.Binding{}, false, invalid("RESOURCE_BINDING_TARGET_NOT_READY", "managed resource must be factually Ready before binding")
	}
	if target.Kind == resourcev1.KindManagedService && target.Type == resourcev1.TypePostgres {
		active, err := s.hasActiveOperation(ctx, projectID, target.ID)
		if err != nil {
			return resourcev1.Binding{}, false, err
		}
		if active {
			return resourcev1.Binding{}, false, Error{Code: "RESOURCE_ACTIVE_OPERATION_CONFLICT", Status: 409, Message: "resource has an active operation"}
		}
	}
	now := s.clock()
	binding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion, ID: newID("rbind"), ProjectID: projectID, EnvironmentID: request.EnvironmentID,
		Source: request.Source, Target: request.Target, Protocol: request.Protocol, LogicalName: logical,
		Lifecycle: resourcev1.LifecycleReady, RuntimeRefs: runtimeRefs(target), CreatedAt: now, UpdatedAt: now,
	}
	postgresBinding := target.Kind == resourcev1.KindManagedService && target.Type == resourcev1.TypePostgres
	if postgresBinding {
		binding.Lifecycle = resourcev1.LifecycleProvisioning
		binding.CredentialID = "rbcred-" + binding.ID
		binding.RoleName = postgresBindingRole(binding.ID)
		binding.Database = target.Runtime.Spec.Connection.Database
		binding.RuntimeRefs = postgresBindingRuntimeRefs(target, binding)
	}
	stored, reused, err := s.Store.CreateBinding(ctx, binding, key, bindingPayload(request))
	if err != nil || !postgresBinding {
		return stored, reused, err
	}
	if s.Credentials == nil {
		return stored, reused, invalid(resourcev1.FailureBindingCredentialUnavailable, "PostgreSQL binding credential authority is unavailable")
	}
	credential, err := s.Credentials.EnsureBinding(ctx, resourcev1.BindingCredentialSpec{CredentialID: stored.CredentialID, BindingID: stored.ID, ResourceID: stored.Target.ID, Username: stored.RoleName, Database: stored.Database})
	if err != nil || credential.ValidateBinding(stored.ID, stored.Target.ID) != nil {
		return stored, reused, invalid(resourcev1.FailureBindingCredentialUnavailable, "PostgreSQL binding credential could not be generated")
	}
	if stored.Lifecycle != resourcev1.LifecycleReady {
		target.Lifecycle = resourcev1.LifecyclePlanned
		target.UpdatedAt = now
		if _, err := s.Store.Update(ctx, target); err != nil {
			return stored, reused, err
		}
	}
	return stored, reused, nil
}

func (s Service) GetBinding(ctx context.Context, projectID, bindingID string) (resourcev1.Binding, error) {
	if s.Store == nil {
		return resourcev1.Binding{}, unavailable()
	}
	return s.Store.GetBinding(ctx, projectID, bindingID)
}

func (s Service) ListBindings(ctx context.Context, projectID, environmentID string) ([]resourcev1.Binding, error) {
	if s.Store == nil {
		return nil, unavailable()
	}
	return s.Store.ListBindings(ctx, projectID, environmentID)
}

func (s Service) DeleteBinding(ctx context.Context, projectID, bindingID string) (resourcev1.Binding, error) {
	binding, err := s.GetBinding(ctx, projectID, bindingID)
	if err != nil {
		return resourcev1.Binding{}, err
	}
	target, err := s.Get(ctx, projectID, binding.Target.ID)
	if err != nil {
		return resourcev1.Binding{}, err
	}
	if target.Kind != resourcev1.KindManagedService || target.Type != resourcev1.TypePostgres {
		return resourcev1.Binding{}, s.Store.DeleteBinding(ctx, projectID, bindingID)
	}
	if binding.Lifecycle != resourcev1.LifecycleDeleting {
		binding.Lifecycle = resourcev1.LifecycleDeleting
		binding.FailureCode = ""
		binding.UpdatedAt = s.clock()
		binding, err = s.Store.UpdateBinding(ctx, binding)
		if err != nil {
			return resourcev1.Binding{}, err
		}
	}
	target.Lifecycle = resourcev1.LifecyclePlanned
	target.UpdatedAt = binding.UpdatedAt
	if _, err := s.Store.Update(ctx, target); err != nil {
		return resourcev1.Binding{}, err
	}
	return binding, nil
}

func postgresBindingRole(bindingID string) string {
	sum := sha256.Sum256([]byte(bindingID))
	return "opsi_b_" + hex.EncodeToString(sum[:16])
}

func postgresBindingRuntimeRefs(target resourcev1.Resource, binding resourcev1.Binding) []resourcev1.RuntimeConnectionReference {
	secret := &resourcev1.SecretReference{SecretID: binding.CredentialID}
	return []resourcev1.RuntimeConnectionReference{
		{Name: "HOST", Sensitivity: resourcev1.ValueNonSecret, Value: target.Runtime.Spec.Connection.Host},
		{Name: "PORT", Sensitivity: resourcev1.ValueNonSecret, Value: strconv.Itoa(int(target.Runtime.Spec.Connection.Port))},
		{Name: "NAME", Sensitivity: resourcev1.ValueNonSecret, Value: binding.Database},
		{Name: "USER", Sensitivity: resourcev1.ValueSecret, SecretRef: secret},
		{Name: "PASSWORD", Sensitivity: resourcev1.ValueSecret, SecretRef: secret},
		{Name: "URL", Sensitivity: resourcev1.ValueSecret, SecretRef: secret},
	}
}

func (s Service) TopologyResources(ctx context.Context, projectID string) ([]resourcev1.Resource, error) {
	return s.List(ctx, projectID, "")
}

func validateCreate(request resourcev1.CreateRequest) error {
	switch request.Kind {
	case resourcev1.KindManagedService:
		if request.Managed == nil || request.External != nil || request.Type != request.Managed.Type {
			return invalid("RESOURCE_SPEC_INVALID", "managed resource requires one matching managed spec")
		}
		return validateManaged(*request.Managed)
	case resourcev1.KindExternalResource:
		if request.External == nil || request.Managed != nil || strings.TrimSpace(string(request.Type)) == "" || strings.TrimSpace(request.Provider) == "" {
			return invalid("RESOURCE_SPEC_INVALID", "external resource requires a type and external spec")
		}
		return validateExternal(*request.External)
	default:
		return invalid("RESOURCE_KIND_INVALID", "resource kind must be managed_service or external_resource")
	}
}

func validateManaged(spec resourcev1.ManagedSpec) error {
	definition, ok := resourcev1.Definition(spec.Type)
	if !ok {
		return invalid("RESOURCE_TYPE_UNSUPPORTED", "managed resource type is unsupported")
	}
	if spec.Replicas < 1 || spec.Replicas > maxReplicas || spec.CPUMillicores < 1 || spec.CPUMillicores > maxCPUMillicores || spec.MemoryBytes < 1 || spec.MemoryBytes > maxMemoryBytes {
		return invalid("RESOURCE_CAPACITY_INVALID", "replicas, CPU, or memory request is invalid")
	}
	if spec.Storage.SizeBytes < 0 || spec.Storage.SizeBytes > resourcev1.MaxManagedStorageBytes || (spec.Storage.Persistent && spec.Storage.SizeBytes == 0) || (!spec.Storage.Persistent && (spec.Storage.SizeBytes != 0 || spec.Storage.PolicyRef != "")) {
		return invalid(resourcev1.FailureStorageInvalid, "persistent storage requires a bounded positive size")
	}
	if definition.Storage.Required && !spec.Storage.Persistent {
		return invalid(resourcev1.FailureStorageRequired, "resource type requires persistent storage")
	}
	if !definition.Storage.Supported && spec.Storage.Persistent {
		return invalid("RESOURCE_STORAGE_UNSUPPORTED", "resource type does not support persistent storage")
	}
	if len(spec.ServiceConfig) != 0 {
		return invalid("RESOURCE_CONFIG_UNSUPPORTED", "resource type has no configurable service keys in P07A")
	}
	if spec.Type == resourcev1.TypePostgres {
		if spec.Storage.PolicyRef != "" && spec.Storage.PolicyRef != resourcev1.StoragePolicyDefault {
			return invalid(resourcev1.FailureStorageInvalid, "managed PostgreSQL storage policy is unsupported")
		}
		if spec.Version != "" && spec.Version != "default" && spec.Version != resourcev1.PostgresVersion {
			return invalid(resourcev1.FailureVersionUpgradeUnsupported, "managed PostgreSQL version changes are unsupported")
		}
	}
	if spec.Type == resourcev1.TypeRedis || spec.Type == resourcev1.TypePostgres {
		if spec.ConnectionPolicy.Mode != "none" && spec.ConnectionPolicy.Mode != "internal" {
			return invalid("RESOURCE_EXPOSURE_INVALID", "managed resource connection policy must be none or internal")
		}
		if len(spec.CredentialRefs) != 0 {
			return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "managed resource credentials are generated by Cloud")
		}
		return nil
	}
	if spec.ConnectionPolicy.Mode != "none" && spec.ConnectionPolicy.Mode != "internal" {
		return invalid("RESOURCE_EXPOSURE_INVALID", "managed resource connection policy must be none or internal")
	}
	for _, ref := range spec.CredentialRefs {
		if !validReference(ref) {
			return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "credential must use an opaque secret reference")
		}
	}
	if len(definition.CredentialKeys) > 0 && len(spec.CredentialRefs) == 0 {
		return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "managed credentials require an opaque secret reference")
	}
	for _, value := range definition.GeneratedValues {
		if value.Sensitivity == resourcev1.ValueSecret && credentialRef(spec.CredentialRefs, value.Name) == nil {
			return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "generated secret values require typed secret references")
		}
	}
	return nil
}

func validateManagedUpdate(current resourcev1.Resource, next resourcev1.ManagedSpec) error {
	if current.Runtime == nil || current.Type != resourcev1.TypePostgres {
		return nil
	}
	version := strings.TrimSpace(next.Version)
	if version != "" && version != "default" && version != current.Runtime.Spec.Version {
		return invalid(resourcev1.FailureVersionUpgradeUnsupported, "managed PostgreSQL version changes are unsupported")
	}
	policy := next.Storage.PolicyRef
	if policy == "" {
		policy = resourcev1.StoragePolicyDefault
	}
	if next.Storage.SizeBytes != current.Runtime.Spec.Storage.SizeBytes || policy != current.Runtime.Spec.Storage.PolicyRef {
		return invalid(resourcev1.FailureStorageResizeUnsupported, "managed PostgreSQL storage resize or policy changes are unsupported")
	}
	return nil
}

func validateExternal(spec resourcev1.ExternalSpec) error {
	if !knownProtocol(spec.Protocol) || strings.TrimSpace(spec.Endpoint) == "" || len(spec.Endpoint) > 253 || spec.Port < 0 || spec.Port > 65535 {
		return invalid("EXTERNAL_RESOURCE_INVALID", "external endpoint or protocol is invalid")
	}
	if spec.CredentialRef != nil && !validReference(*spec.CredentialRef) {
		return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "credential must use an opaque secret reference")
	}
	if spec.TLS.Mode != "" && spec.TLS.Mode != "disabled" && spec.TLS.Mode != "secret_ref" {
		return invalid("EXTERNAL_TLS_INVALID", "TLS mode is invalid")
	}
	if spec.TLS.Mode == "secret_ref" && (spec.TLS.SecretRef == nil || !validReference(*spec.TLS.SecretRef)) {
		return invalid("RESOURCE_SECRET_REFERENCE_INVALID", "TLS requires an opaque secret reference")
	}
	return nil
}

func legacyRuntimeRefs(target resourcev1.Resource) []resourcev1.RuntimeConnectionReference {
	if target.Kind == resourcev1.KindExternalResource {
		refs := []resourcev1.RuntimeConnectionReference{{Name: "HOST", Sensitivity: resourcev1.ValueNonSecret, Value: target.External.Endpoint}}
		if target.External.Port > 0 {
			refs = append(refs, resourcev1.RuntimeConnectionReference{Name: "PORT", Sensitivity: resourcev1.ValueNonSecret, Value: strconv.Itoa(target.External.Port)})
		}
		if target.External.CredentialRef != nil {
			refs = append(refs, resourcev1.RuntimeConnectionReference{Name: "CREDENTIALS", Sensitivity: resourcev1.ValueSecret, SecretRef: cloneSecret(target.External.CredentialRef)})
		}
		return refs
	}
	definition, _ := resourcev1.Definition(target.Type)
	refs := make([]resourcev1.RuntimeConnectionReference, 0, len(definition.GeneratedValues))
	for _, value := range definition.GeneratedValues {
		ref := resourcev1.RuntimeConnectionReference{Name: value.Name, Sensitivity: value.Sensitivity}
		switch value.Name {
		case "HOST":
			ref.Value = target.InternalName
		case "PORT":
			ref.Value = strconv.Itoa(definition.DefaultPort)
		case "URL":
			if value.Sensitivity == resourcev1.ValueNonSecret {
				ref.Value = string(definition.Protocols[0]) + "://" + target.InternalName + ":" + strconv.Itoa(definition.DefaultPort)
			} else {
				ref.SecretRef = credentialRef(target.Managed.CredentialRefs, value.Name)
			}
		default:
			ref.SecretRef = credentialRef(target.Managed.CredentialRefs, value.Name)
		}
		refs = append(refs, ref)
	}
	return refs
}

func credentialRef(refs []resourcev1.SecretReference, name string) *resourcev1.SecretReference {
	for _, ref := range refs {
		if strings.EqualFold(ref.Key, name) {
			value := ref
			return &value
		}
	}
	if len(refs) > 0 {
		value := refs[0]
		if value.Key != "" {
			return nil
		}
		value.Key = strings.ToLower(name)
		return &value
	}
	return nil
}

func protocolCompatible(target resourcev1.Resource, protocol resourcev1.Protocol) bool {
	if target.Kind == resourcev1.KindExternalResource {
		return target.External != nil && target.External.Protocol == protocol
	}
	return resourcev1.Supports(target.Type, protocol)
}

func knownProtocol(value resourcev1.Protocol) bool {
	switch value {
	case resourcev1.ProtocolPostgres, resourcev1.ProtocolRedis, resourcev1.ProtocolNATS, resourcev1.ProtocolAMQP, resourcev1.ProtocolMySQL, resourcev1.ProtocolHTTP, resourcev1.ProtocolTCP, resourcev1.ProtocolCustom:
		return true
	}
	return false
}

func validReference(ref resourcev1.SecretReference) bool {
	return ref.SecretID != "" && len(ref.SecretID) <= 128 && !strings.ContainsAny(ref.SecretID, " /\\\t\r\n") && len(ref.Key) <= 64
}

func validKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if r <= 32 || r > 126 {
			return false
		}
	}
	return true
}

func createPayload(request resourcev1.CreateRequest) string         { return payloadHash(request) }
func bindingPayload(request resourcev1.CreateBindingRequest) string { return payloadHash(request) }

func payloadHash(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func cloneManaged(value *resourcev1.ManagedSpec) *resourcev1.ManagedSpec {
	if value == nil {
		return nil
	}
	out := *value
	out.ServiceConfig = cloneMap(value.ServiceConfig)
	out.CredentialRefs = append([]resourcev1.SecretReference(nil), value.CredentialRefs...)
	return &out
}

func cloneExternal(value *resourcev1.ExternalSpec) *resourcev1.ExternalSpec {
	if value == nil {
		return nil
	}
	out := *value
	out.CredentialRef = cloneSecret(value.CredentialRef)
	out.TLS.SecretRef = cloneSecret(value.TLS.SecretRef)
	return &out
}

func cloneSecret(value *resourcev1.SecretReference) *resourcev1.SecretReference {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func newID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}

func invalid(code, message string) Error { return Error{Code: code, Status: 400, Message: message} }
func unavailable() Error {
	return Error{Code: "RESOURCE_UNAVAILABLE", Status: 503, Message: "resource authority is unavailable"}
}
