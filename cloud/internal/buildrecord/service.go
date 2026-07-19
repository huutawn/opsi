package buildrecord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

var (
	sha40Pattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hashPattern          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	serviceKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	platformPattern      = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(?:/[a-z0-9_.-]+)?$`)
	ociRepositoryPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)
)

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Code }

type Binding struct {
	ProjectID          string
	BindingID          string
	ServiceID          string
	ServiceKey         string
	RepositoryID       uint64
	RepositoryOwnerID  uint64
	RepositoryFullName string
}

type BindingResolver interface {
	ResolveBuildBinding(ctx context.Context, repositoryID uint64, serviceKey string) (Binding, error)
}

type ListFilter struct {
	ServiceKey   string
	RepositoryID uint64
	SHA          string
	Status       string
	Limit        int
	Cursor       string
}

type Store interface {
	Create(ctx context.Context, payloadHash string, record buildrecordv1.Record) (buildrecordv1.Record, bool, error)
	List(ctx context.Context, projectID string, filter ListFilter) (buildrecordv1.ListResult, error)
	Get(ctx context.Context, projectID, recordID string) (buildrecordv1.Record, error)
}

type AuditEvent struct {
	ProjectID    string
	RecordID     string
	RepositoryID uint64
	RunID        uint64
	RunAttempt   uint32
	ServiceKey   string
	SHA          string
	ConfigHash   string
	OCIDigest    string
	Result       string
}

type Service struct {
	Store     Store
	Bindings  BindingResolver
	Policies  []githuboidc.WorkloadPolicy
	Now       func() time.Time
	NewID     func() (string, error)
	AuditSink func(AuditEvent)
}

func (s Service) Submit(ctx context.Context, identity githuboidc.VerifiedIdentity, request buildrecordv1.Submission) (buildrecordv1.Record, bool, error) {
	if s.Store == nil || s.Bindings == nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	if err := validateSubmission(identity, request); err != nil {
		return buildrecordv1.Record{}, false, err
	}
	binding, err := s.Bindings.ResolveBuildBinding(ctx, identity.RepositoryID, request.ServiceKey)
	if err != nil {
		return buildrecordv1.Record{}, false, Error{Code: "BUILD_BINDING_INVALID", Status: 403, Message: "active GitHub repository/service binding is required"}
	}
	if binding.RepositoryID != identity.RepositoryID || binding.RepositoryOwnerID != identity.RepositoryOwnerID || binding.ServiceKey != request.ServiceKey {
		return buildrecordv1.Record{}, false, Error{Code: "BUILD_BINDING_MISMATCH", Status: 403, Message: "verified workload does not match the active repository binding"}
	}
	if !policyAllows(s.Policies, identity, request) {
		return buildrecordv1.Record{}, false, Error{Code: "BUILD_WORKLOAD_FORBIDDEN", Status: 403, Message: "workflow, ref, event, service, or OCI repository is not allowlisted"}
	}
	id, err := s.newID()
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	createdAt := time.Now().UTC()
	if s.Now != nil {
		createdAt = s.Now().UTC()
	}
	record := buildrecordv1.Record{
		SchemaVersion: buildrecordv1.SchemaVersion, ID: id, ProjectID: binding.ProjectID,
		RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID,
		ActiveBindingID: binding.BindingID, ServiceID: binding.ServiceID, ServiceKey: binding.ServiceKey, CreatedAt: createdAt,
		Workload: buildrecordv1.WorkloadIdentity{Issuer: identity.Issuer, Subject: identity.Subject, RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID, Ref: identity.Ref, SHA: identity.SHA, EventName: identity.EventName, Workflow: identity.Workflow, WorkflowRef: identity.WorkflowRef, JobWorkflowRef: identity.JobWorkflowRef, RunID: identity.RunID, RunAttempt: identity.RunAttempt},
		Build:    buildrecordv1.BuildMetadata{ConfigHash: request.ConfigHash, PlanHash: request.PlanHash, Platform: request.Platform, OCIRepository: request.OCIRepository, OCIDigest: request.OCIDigest, ProvenanceDigest: request.ProvenanceDigest, Status: request.Status},
	}
	payloadRecord := record
	payloadRecord.ID = ""
	payloadRecord.CreatedAt = time.Time{}
	payload, err := json.Marshal(payloadRecord)
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	sum := sha256.Sum256(payload)
	result, reused, storeErr := s.Store.Create(ctx, hex.EncodeToString(sum[:]), record)
	if s.AuditSink != nil {
		resultID := ""
		if storeErr == nil {
			resultID = record.ID
			if result.ID != "" {
				resultID = result.ID
			}
		}
		outcome := "created"
		if storeErr != nil {
			outcome = "rejected"
		} else if reused {
			outcome = "reused"
		}
		s.AuditSink(AuditEvent{ProjectID: record.ProjectID, RecordID: resultID, RepositoryID: identity.RepositoryID, RunID: identity.RunID, RunAttempt: identity.RunAttempt, ServiceKey: request.ServiceKey, SHA: identity.SHA, ConfigHash: request.ConfigHash, OCIDigest: request.OCIDigest, Result: outcome})
	}
	return result, reused, storeErr
}

func (s Service) List(ctx context.Context, projectID string, filter ListFilter) (buildrecordv1.ListResult, error) {
	if s.Store == nil || !validOpaqueID(projectID) {
		return buildrecordv1.ListResult{}, invalid("BUILD_RECORD_LIST_INVALID", "project or filter is invalid")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 || filter.RepositoryID > uint64(1<<63-1) || (filter.ServiceKey != "" && !serviceKeyPattern.MatchString(filter.ServiceKey)) || (filter.SHA != "" && !sha40Pattern.MatchString(filter.SHA)) || (filter.Status != "" && filter.Status != "succeeded") {
		return buildrecordv1.ListResult{}, invalid("BUILD_RECORD_LIST_INVALID", "project or filter is invalid")
	}
	return s.Store.List(ctx, projectID, filter)
}

func (s Service) Get(ctx context.Context, projectID, recordID string) (buildrecordv1.Record, error) {
	if s.Store == nil || !validOpaqueID(projectID) || !validOpaqueID(recordID) {
		return buildrecordv1.Record{}, invalid("BUILD_RECORD_ID_INVALID", "project_id and record_id are invalid")
	}
	return s.Store.Get(ctx, projectID, recordID)
}

func validateSubmission(identity githuboidc.VerifiedIdentity, request buildrecordv1.Submission) error {
	if request.SchemaVersion != buildrecordv1.SchemaVersion || !serviceKeyPattern.MatchString(request.ServiceKey) || request.Status != "succeeded" {
		return invalid("BUILD_RECORD_INVALID", "build record schema, service, or status is invalid")
	}
	if request.RepositoryID != identity.RepositoryID || request.RepositoryOwnerID != identity.RepositoryOwnerID || request.Ref != identity.Ref || request.SHA != identity.SHA || request.EventName != identity.EventName || request.WorkflowRef != identity.WorkflowRef || request.JobWorkflowRef != identity.JobWorkflowRef || request.RunID != identity.RunID || request.RunAttempt != identity.RunAttempt {
		return Error{Code: "BUILD_CLAIM_BODY_MISMATCH", Status: 403, Message: "build identity fields do not match the verified OIDC claims"}
	}
	if !sha40Pattern.MatchString(request.SHA) || !hashPattern.MatchString(request.ConfigHash) || (request.PlanHash != "" && !hashPattern.MatchString(request.PlanHash)) || !platformPattern.MatchString(request.Platform) || len(request.Platform) > 64 {
		return invalid("BUILD_METADATA_INVALID", "SHA, config hash, plan hash, or platform is invalid")
	}
	if !canonicalOCIRepository(request.OCIRepository) || !digestPattern.MatchString(request.OCIDigest) || (request.ProvenanceDigest != "" && !digestPattern.MatchString(request.ProvenanceDigest)) {
		return invalid("BUILD_ARTIFACT_INVALID", "OCI repository or immutable digest is invalid")
	}
	return nil
}

func policyAllows(policies []githuboidc.WorkloadPolicy, identity githuboidc.VerifiedIdentity, request buildrecordv1.Submission) bool {
	for _, policy := range policies {
		if policy.RepositoryID != identity.RepositoryID || policy.ServiceKey != request.ServiceKey || !contains(policy.WorkflowRefs, identity.WorkflowRef) || !contains(policy.Refs, identity.Ref) || !contains(policy.Events, identity.EventName) || !contains(policy.OCIRepositories, request.OCIRepository) {
			continue
		}
		if len(policy.JobWorkflowRefs) > 0 && !contains(policy.JobWorkflowRefs, identity.JobWorkflowRef) {
			continue
		}
		if len(policy.JobWorkflowRefs) == 0 && identity.JobWorkflowRef != "" {
			continue
		}
		return true
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func canonicalOCIRepository(value string) bool {
	return len(value) <= 255 && value == strings.ToLower(value) && ociRepositoryPattern.MatchString(value) && !strings.Contains(value, "@")
}
func validOpaqueID(value string) bool {
	return len(value) > 0 && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n/\\")
}
func invalid(code, message string) error { return Error{Code: code, Status: 400, Message: message} }
func unavailable() error {
	return Error{Code: "BUILD_RECORD_UNAVAILABLE", Status: 503, Message: "build record service is unavailable"}
}

func (s Service) newID() (string, error) {
	if s.NewID != nil {
		return s.NewID()
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "br-" + hex.EncodeToString(raw[:]), nil
}

type memoryEntry struct {
	identityKey, payloadHash string
	record                   buildrecordv1.Record
}
type MemoryStore struct {
	mu         sync.Mutex
	byIdentity map[string]memoryEntry
	byID       map[string]memoryEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byIdentity: map[string]memoryEntry{}, byID: map[string]memoryEntry{}}
}
func (s *MemoryStore) Create(_ context.Context, payloadHash string, record buildrecordv1.Record) (buildrecordv1.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identityKey := fmt.Sprintf("%d:%d:%d:%s", record.RepositoryID, record.Workload.RunID, record.Workload.RunAttempt, record.ServiceKey)
	if current, ok := s.byIdentity[identityKey]; ok {
		if current.payloadHash != payloadHash {
			return buildrecordv1.Record{}, false, Error{Code: "BUILD_RECORD_CONFLICT", Status: 409, Message: "build identity was already submitted with different metadata"}
		}
		return current.record, true, nil
	}
	entry := memoryEntry{identityKey: identityKey, payloadHash: payloadHash, record: record}
	s.byIdentity[identityKey] = entry
	s.byID[record.ID] = entry
	return record, false, nil
}
func (s *MemoryStore) List(_ context.Context, projectID string, filter ListFilter) (buildrecordv1.ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]buildrecordv1.Record, 0)
	for _, entry := range s.byID {
		record := entry.record
		if record.ProjectID != projectID || (filter.ServiceKey != "" && record.ServiceKey != filter.ServiceKey) || (filter.RepositoryID != 0 && record.RepositoryID != filter.RepositoryID) || (filter.SHA != "" && record.Workload.SHA != filter.SHA) || (filter.Status != "" && record.Build.Status != filter.Status) {
			continue
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if filter.Cursor != "" {
		cursor, err := decodeCursor(filter.Cursor)
		if err != nil {
			return buildrecordv1.ListResult{}, invalid("BUILD_RECORD_CURSOR_INVALID", "cursor is invalid")
		}
		start := 0
		for start < len(items) && (items[start].CreatedAt.After(cursor.Time) || (items[start].CreatedAt.Equal(cursor.Time) && items[start].ID >= cursor.ID)) {
			start++
		}
		items = items[start:]
	}
	result := buildrecordv1.ListResult{Records: []buildrecordv1.Record{}}
	if len(items) > filter.Limit {
		result.Records = append(result.Records, items[:filter.Limit]...)
		last := result.Records[len(result.Records)-1]
		result.NextCursor = encodeCursor(cursorValue{Time: last.CreatedAt, ID: last.ID})
	} else {
		result.Records = append(result.Records, items...)
	}
	return result, nil
}
func (s *MemoryStore) Get(_ context.Context, projectID, recordID string) (buildrecordv1.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byID[recordID]
	if !ok || entry.record.ProjectID != projectID {
		return buildrecordv1.Record{}, Error{Code: "BUILD_RECORD_NOT_FOUND", Status: 404, Message: "build record was not found"}
	}
	return entry.record, nil
}

type cursorValue struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func encodeCursor(value cursorValue) string {
	data, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeCursor(value string) (cursorValue, error) {
	var cursor cursorValue
	if len(value) > 1024 {
		return cursorValue{}, errors.New("cursor invalid")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(data) > 512 || json.Unmarshal(data, &cursor) != nil || cursor.Time.IsZero() || !validOpaqueID(cursor.ID) {
		return cursorValue{}, errors.New("cursor invalid")
	}
	cursor.Time = cursor.Time.UTC()
	return cursor, nil
}
