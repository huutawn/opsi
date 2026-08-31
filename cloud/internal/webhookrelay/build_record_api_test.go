package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type buildRecordOIDCFixture struct{ identity githuboidc.VerifiedIdentity }

func (f buildRecordOIDCFixture) Verify(_ context.Context, token string) (githuboidc.VerifiedIdentity, error) {
	if token != "synthetic-oidc-token" {
		return githuboidc.VerifiedIdentity{}, errors.New("invalid")
	}
	return f.identity, nil
}

type fixedRateLimiter struct{ allow bool }

func (l fixedRateLimiter) Allow(string, int, time.Duration) bool { return l.allow }

type replayRejectingBuildRecordOIDC struct {
	identity githuboidc.VerifiedIdentity
	mu       sync.Mutex
	seen     map[string]bool
}

func (f *replayRejectingBuildRecordOIDC) Verify(_ context.Context, token string) (githuboidc.VerifiedIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[token] {
		return githuboidc.VerifiedIdentity{}, errors.New("replayed")
	}
	f.seen[token] = true
	return f.identity, nil
}

type flakyAutomaticDeliveryRegistry struct {
	registry.API
	starter      immutableDeploymentStarter
	repositories []registry.GitHubRepository
	failures     atomic.Int32
}

type automaticDeliveryFacts struct{ facts topology.Facts }

func (f automaticDeliveryFacts) PlacementFacts(context.Context, string) (topology.Facts, error) {
	return f.facts, nil
}

func (r *flakyAutomaticDeliveryRegistry) ListGitHubRepositories(string) ([]registry.GitHubRepository, error) {
	return r.repositories, nil
}

func (r *flakyAutomaticDeliveryRegistry) StartImmutableDeployment(snapshot deploymentv1.JobSnapshot, requestedBy, key, requestID string) (registry.DeploymentJob, bool, error) {
	if r.failures.Add(-1) >= 0 {
		return registry.DeploymentJob{}, false, errors.New("temporary store failure secret-marker")
	}
	return r.starter.StartImmutableDeployment(snapshot, requestedBy, key, requestID)
}

type blockingBuildRecordOIDC struct {
	identity githuboidc.VerifiedIdentity
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
}

func (f *blockingBuildRecordOIDC) Verify(ctx context.Context, _ string) (githuboidc.VerifiedIdentity, error) {
	f.calls.Add(1)
	select {
	case f.entered <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return f.identity, nil
	case <-ctx.Done():
		return githuboidc.VerifiedIdentity{}, ctx.Err()
	}
}

func TestBuildRecordSubmissionRateLimitRunsBeforeOIDCAndDoesNotLeakToken(t *testing.T) {
	identity := testBuildRecordIdentity()
	verifier := &blockingBuildRecordOIDC{identity: identity, entered: make(chan struct{}, 1), release: make(chan struct{})}
	server := NewServer(Config{})
	server.OIDC = verifier
	server.limits = fixedRateLimiter{allow: false}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response := postBuildRecord(t, httpServer.URL, mustBuildRecordBody(t, identity), "rate-limit-secret-marker")
	raw := readResponse(response)
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "60" || !strings.Contains(raw, "BUILD_RECORD_RATE_LIMITED") {
		t.Fatalf("status=%d retry=%q body=%s", response.StatusCode, response.Header.Get("Retry-After"), raw)
	}
	if verifier.calls.Load() != 0 || strings.Contains(raw, "rate-limit-secret-marker") {
		t.Fatalf("verifier calls=%d body=%s", verifier.calls.Load(), raw)
	}
}

func TestBuildRecordSubmissionConcurrencyIsBoundedBeforeOIDCWork(t *testing.T) {
	identity := testBuildRecordIdentity()
	verifier := &blockingBuildRecordOIDC{identity: identity, entered: make(chan struct{}, 1), release: make(chan struct{})}
	server := NewServer(Config{})
	server.OIDC = verifier
	server.limits = fixedRateLimiter{allow: true}
	server.buildRecordSlots = make(chan struct{}, 1)
	server.BuildRecords = buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: testBuildBindingResolver{binding: buildrecord.Binding{ProjectID: "project-1", BindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID}}, Policies: []githuboidc.WorkloadPolicy{{RepositoryID: identity.RepositoryID, ServiceKey: "api", WorkflowRefs: []string{identity.WorkflowRef}, Refs: []string{identity.Ref}, Events: []string{identity.EventName}, OCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}}}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	firstDone := make(chan *http.Response, 1)
	body := mustBuildRecordBody(t, identity)
	go func() { firstDone <- postBuildRecord(t, httpServer.URL, body, "first-token") }()
	select {
	case <-verifier.entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter verifier")
	}
	second := postBuildRecord(t, httpServer.URL, body, "second-token")
	raw := readResponse(second)
	if second.StatusCode != http.StatusTooManyRequests || second.Header.Get("Retry-After") != "1" || !strings.Contains(raw, "BUILD_RECORD_BUSY") {
		t.Fatalf("status=%d retry=%q body=%s", second.StatusCode, second.Header.Get("Retry-After"), raw)
	}
	if verifier.calls.Load() != 1 {
		t.Fatalf("verifier calls=%d", verifier.calls.Load())
	}
	close(verifier.release)
	response := <-firstDone
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", response.StatusCode, readResponse(response))
	}
}

func TestBuildRecordSubmissionOIDCStrictReplayAndConflict(t *testing.T) {
	identity := testBuildRecordIdentity()
	server := NewServer(Config{})
	server.OIDC = buildRecordOIDCFixture{identity: identity}
	server.BuildRecords = buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: testBuildBindingResolver{binding: buildrecord.Binding{ProjectID: "project-1", BindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID}}, Policies: []githuboidc.WorkloadPolicy{{RepositoryID: identity.RepositoryID, ServiceKey: "api", WorkflowRefs: []string{identity.WorkflowRef}, Refs: []string{identity.Ref}, Events: []string{identity.EventName}, OCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}}}, NewID: func() (string, error) { return "br-http", nil }}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	submission := testBuildRecordSubmission(identity)
	body, _ := json.Marshal(submission)
	first := postBuildRecord(t, httpServer.URL, body, "synthetic-oidc-token")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.StatusCode, readResponse(first))
	}
	var created struct {
		Record buildrecordv1.Record `json:"record"`
		Reused bool                 `json:"reused"`
	}
	decodeResponse(t, first, &created)
	if created.Reused || created.Record.ProjectID != "project-1" {
		t.Fatalf("created=%+v", created)
	}
	replay := postBuildRecord(t, httpServer.URL, body, "synthetic-oidc-token")
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.StatusCode, readResponse(replay))
	}
	var replayed struct {
		Record buildrecordv1.Record `json:"record"`
		Reused bool                 `json:"reused"`
	}
	decodeResponse(t, replay, &replayed)
	if !replayed.Reused || replayed.Record.ID != created.Record.ID {
		t.Fatalf("replayed=%+v", replayed)
	}
	submission.OCIDigest = "sha256:" + strings.Repeat("d", 64)
	conflictBody, _ := json.Marshal(submission)
	conflict := postBuildRecord(t, httpServer.URL, conflictBody, "synthetic-oidc-token")
	assertBuildRecordAPIError(t, conflict, http.StatusConflict, "BUILD_RECORD_CONFLICT")
	unknown := append(body[:len(body)-1], []byte(`,"unknown":true}`)...)
	invalid := postBuildRecord(t, httpServer.URL, unknown, "synthetic-oidc-token")
	assertBuildRecordAPIError(t, invalid, http.StatusBadRequest, "INVALID_JSON")
	pat := postBuildRecord(t, httpServer.URL, body, "human-pat")
	assertBuildRecordAPIError(t, pat, http.StatusUnauthorized, "OIDC_AUTH_INVALID")

	missingAuth, err := http.Post(httpServer.URL+"/v1/build-records", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	assertBuildRecordAPIError(t, missingAuth, http.StatusUnauthorized, "OIDC_AUTH_REQUIRED")

	queryRequest, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/build-records?token=forbidden", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	queryRequest.Header.Set("Authorization", "Bearer synthetic-oidc-token")
	queryResponse, err := http.DefaultClient.Do(queryRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertBuildRecordAPIError(t, queryResponse, http.StatusBadRequest, "OIDC_REQUEST_INVALID")

	cookieRequest, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/build-records", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	cookieRequest.Header.Set("Authorization", "Bearer synthetic-oidc-token")
	cookieRequest.AddCookie(&http.Cookie{Name: "oidc", Value: "forbidden"})
	cookieResponse, err := http.DefaultClient.Do(cookieRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertBuildRecordAPIError(t, cookieResponse, http.StatusBadRequest, "OIDC_REQUEST_INVALID")
}

func TestBuildRecordSubmissionReportsPendingAutomaticDeliveryAndReplaysExactly(t *testing.T) {
	server, store, projectID, identity := automaticDeliveryServer(t, 1)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body := mustBuildRecordBody(t, identity)

	first := postBuildRecord(t, httpServer.URL, body, "oidc-token-1")
	assertBuildRecordAPIError(t, first, http.StatusServiceUnavailable, "AUTOMATIC_DELIVERY_PENDING")
	if jobs, err := store.ListDeployments(projectID); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if _, err := server.BuildRecords.Get(context.Background(), projectID, "br-auto"); err != nil {
		t.Fatalf("accepted BuildRecord was rolled back: %v", err)
	}
	replayedOIDC := postBuildRecord(t, httpServer.URL, body, "oidc-token-1")
	assertBuildRecordAPIError(t, replayedOIDC, http.StatusUnauthorized, "OIDC_AUTH_INVALID")

	second := postBuildRecord(t, httpServer.URL, body, "oidc-token-2")
	if second.StatusCode != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", second.StatusCode, readResponse(second))
	}
	var retried struct {
		Record   buildrecordv1.Record    `json:"record"`
		Reused   bool                    `json:"reused"`
		Delivery *registry.DeploymentJob `json:"delivery"`
	}
	decodeResponse(t, second, &retried)
	if !retried.Reused || retried.Record.ID != "br-auto" || retried.Delivery == nil {
		t.Fatalf("retry=%+v", retried)
	}

	third := postBuildRecord(t, httpServer.URL, body, "oidc-token-3")
	var replayed struct {
		Delivery *registry.DeploymentJob `json:"delivery"`
	}
	decodeResponse(t, third, &replayed)
	if third.StatusCode != http.StatusOK || replayed.Delivery == nil || replayed.Delivery.ID != retried.Delivery.ID || !replayed.Delivery.Reused {
		t.Fatalf("replay status=%d delivery=%+v", third.StatusCode, replayed.Delivery)
	}
	jobs, err := store.ListDeployments(projectID)
	if err != nil || len(jobs) != 1 || jobs[0].ID != retried.Delivery.ID {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	audits, err := store.ListAudit(projectID)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, audit := range audits {
		if audit.Action == "MAIN_DEPLOYMENT_CREATED" && audit.ResourceID == retried.Delivery.ID {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("creation audits=%d audits=%+v", created, audits)
	}
}

func TestBuildRecordSubmissionAcceptsWhenAutomaticRoutingNeedsTopology(t *testing.T) {
	server, store, projectID, identity := automaticDeliveryServer(t, 0)
	facts := server.Topology.Facts
	withoutTopology := topology.Service{Store: topology.NewMemoryStore(), Facts: facts, Now: server.Topology.Now}
	server.Topology = withoutTopology
	server.Policies.Topology = withoutTopology
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response := postBuildRecord(t, httpServer.URL, mustBuildRecordBody(t, identity), "oidc-topology-missing")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.StatusCode, readResponse(response))
	}
	var accepted struct {
		Record   buildrecordv1.Record    `json:"record"`
		Delivery *registry.DeploymentJob `json:"delivery"`
	}
	decodeResponse(t, response, &accepted)
	if accepted.Record.ID != "br-auto" || accepted.Delivery != nil {
		t.Fatalf("accepted=%+v", accepted)
	}
	if _, err := server.BuildRecords.Get(context.Background(), projectID, "br-auto"); err != nil {
		t.Fatalf("accepted BuildRecord missing: %v", err)
	}
	if jobs, err := store.ListDeployments(projectID); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestBuildRecordSubmissionConcurrentAutomaticReplayCreatesOneJobAndAudit(t *testing.T) {
	server, store, projectID, identity := automaticDeliveryServer(t, 0)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body := mustBuildRecordBody(t, identity)
	type concurrentResponse struct {
		response *http.Response
		err      error
	}
	responses := make(chan concurrentResponse, 2)
	var group sync.WaitGroup
	for _, token := range []string{"oidc-concurrent-1", "oidc-concurrent-2"} {
		group.Add(1)
		go func(token string) {
			defer group.Done()
			request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/build-records", bytes.NewReader(body))
			if err != nil {
				responses <- concurrentResponse{err: err}
				return
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			responses <- concurrentResponse{response: response, err: err}
		}(token)
	}
	group.Wait()
	close(responses)
	for item := range responses {
		if item.err != nil {
			t.Fatal(item.err)
		}
		response := item.response
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
			t.Fatalf("concurrent status=%d body=%s", response.StatusCode, readResponse(response))
		}
		_ = readResponse(response)
	}
	jobs, err := store.ListDeployments(projectID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	audits, err := store.ListAudit(projectID)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, audit := range audits {
		if audit.Action == "MAIN_DEPLOYMENT_CREATED" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("creation audits=%d audits=%+v", created, audits)
	}
}

func TestBuildRecordSubmissionRejectsClaimMismatchAndDoesNotReflectToken(t *testing.T) {
	identity := testBuildRecordIdentity()
	server := NewServer(Config{})
	server.OIDC = buildRecordOIDCFixture{identity: identity}
	server.BuildRecords = buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: testBuildBindingResolver{binding: buildrecord.Binding{ProjectID: "project-1", BindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 8}}, Policies: []githuboidc.WorkloadPolicy{{RepositoryID: 7, ServiceKey: "api", WorkflowRefs: []string{identity.WorkflowRef}, Refs: []string{identity.Ref}, Events: []string{"push"}, OCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}}}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	submission := testBuildRecordSubmission(identity)
	submission.SHA = strings.Repeat("d", 40)
	body, _ := json.Marshal(submission)
	response := postBuildRecord(t, httpServer.URL, body, "synthetic-oidc-token")
	raw := readResponse(response)
	if response.StatusCode != http.StatusForbidden || !strings.Contains(raw, "BUILD_CLAIM_BODY_MISMATCH") {
		t.Fatalf("status=%d body=%s", response.StatusCode, raw)
	}
	if strings.Contains(raw, "synthetic-oidc-token") {
		t.Fatal("response reflected OIDC token")
	}
}

func TestBuildRecordReadIsPATAuthenticatedAndProjectScoped(t *testing.T) {
	ownerHash, _ := auth.HashPAT("owner-pat")
	otherHash, _ := auth.HashPAT("other-pat")
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{UserID: "owner", OrgID: "org-1", Role: "Owner", Hash: ownerHash}, {UserID: "other", OrgID: "org-2", Role: "Owner", Hash: otherHash}}}}
	handler := server.Handler()
	projectA := createProjectWithToken(t, handler, "org-1", "owner-pat", "br-project-a")
	projectB := createProjectWithToken(t, handler, "org-2", "other-pat", "br-project-b")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{UserID: "owner", OrgID: "org-1", ProjectID: projectA, Role: "Owner", Hash: ownerHash}, {UserID: "other", OrgID: "org-2", ProjectID: projectB, Role: "Owner", Hash: otherHash}}}}
	record := testBuildRecordIdentityRecord(projectA)
	if _, _, err := server.BuildRecords.Store.Create(context.Background(), strings.Repeat("a", 64), record); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectA+"/build-records", nil)
	request.Header.Set("Authorization", "Bearer owner-pat")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), record.ID) {
		t.Fatalf("owner status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectA+"/build-records", nil)
	request.Header.Set("Authorization", "Bearer other-pat")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-project status=%d body=%s", response.Code, response.Body.String())
	}
}

type testBuildBindingResolver struct{ binding buildrecord.Binding }

func (r testBuildBindingResolver) ResolveBuildBinding(context.Context, uint64, string) (buildrecord.Binding, error) {
	return r.binding, nil
}

func automaticDeliveryServer(t *testing.T, failures int32) (*Server, *registry.Service, string, githuboidc.VerifiedIdentity) {
	t.Helper()
	now := time.Now().UTC()
	identity := testBuildRecordIdentity()
	identity.Subject = "repo:huutawn/opsi:ref:refs/heads/main"
	identity.Ref = "refs/heads/main"
	identity.WorkflowRef = "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/main"
	server := NewServer(Config{})
	store := server.Registry.(*registry.Service)
	project, err := store.CreateProject("org-1", "Automatic", "automatic", "owner", "project-auto")
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.UpsertNode(project.ID, "node-1", "server", registry.NodeHealthy, "203.0.113.10", "", "node-auto")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.RegisterAgent(project.ID, node.ID, "sha256:test", "credential", "v1", "agent-auto", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	service, err := store.CreateService(project.ID, registry.ServiceDraft{Name: "api", RepoURL: "https://github.com/huutawn/opsi", ContainerPort: 8080, ResourceLimits: map[string]string{"cpu": "500m", "memory": "512Mi"}}, "service-auto")
	if err != nil {
		t.Fatal(err)
	}
	binding := testBuildBindingResolver{binding: buildrecord.Binding{ProjectID: project.ID, BindingID: "binding-auto", ServiceID: service.ID, ServiceKey: service.Name, RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID}}
	server.BuildRecords = buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: binding, Policies: []githuboidc.WorkloadPolicy{{RepositoryID: identity.RepositoryID, ServiceKey: service.Name, WorkflowRefs: []string{identity.WorkflowRef}, Refs: []string{identity.Ref}, Events: []string{identity.EventName}, OCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}}}, NewID: func() (string, error) { return "br-auto", nil }}
	facts, err := store.PlacementFacts(context.Background(), project.ID)
	if err != nil || len(facts.Environments) != 1 || len(facts.Runtimes) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	fresh := now
	facts.Services = []topology.ServiceFact{{ID: service.ID, ProjectID: project.ID, Key: service.Name}}
	facts.Nodes = []topology.NodeFact{{ID: node.ID, ProjectID: project.ID, RuntimeID: service.RuntimeID, Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &fresh}}
	facts.Agents = []topology.AgentFact{{ID: agent.ID, ProjectID: project.ID, RuntimeID: service.RuntimeID, NodeID: node.ID, Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh}}
	server.Topology = topology.Service{Store: topology.NewMemoryStore(), Facts: automaticDeliveryFacts{facts}, Now: func() time.Time { return now }}
	assignment := topologyv1.Assignment{ServiceKey: service.Name, EnvironmentID: facts.Environments[0].ID, RuntimeID: facts.Runtimes[0].ID, Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 64 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}
	topologyDraft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: project.ID, Assignments: []topologyv1.Assignment{assignment}}
	if validation, err := server.Topology.Validate(context.Background(), project.ID, topologyDraft, false); err != nil || !validation.Valid {
		t.Fatalf("topology validation=%+v err=%v", validation, err)
	}
	if _, err := server.Topology.Apply(context.Background(), project.ID, "owner", "topology-auto", topologyv1.ApplyRequest{Draft: topologyDraft}, false); err != nil {
		t.Fatal(err)
	}
	server.Policies = deploymentpolicy.Service{Store: deploymentpolicy.NewMemoryStore(), BuildRecords: server.BuildRecords.Store, Bindings: binding, Topology: server.Topology, Now: func() time.Time { return now }}
	draft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: project.ID, RepositoryID: identity.RepositoryID, ServiceKeys: []string{service.Name}, WorkflowRefs: []string{identity.WorkflowRef}, AllowedEvents: []string{"push"}, AllowedGitRefs: []string{identity.Ref}, EnvironmentID: assignment.EnvironmentID, AllowedRuntimeIDs: []string{assignment.RuntimeID}, AllowedOCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}, AllowedPlatforms: []string{"linux/amd64"}, AllowedConfigHashes: []string{strings.Repeat("b", 64)}, AllowedBuildPlanHashes: []string{strings.Repeat("d", 64)}, AutomaticMain: true, AllowUnknownCapacity: true, Enabled: true}
	if _, err := server.Policies.Apply(context.Background(), project.ID, "owner", "policy-auto", deploymentpolicyv1.ApplyRequest{Draft: draft}); err != nil {
		t.Fatal(err)
	}
	flaky := &flakyAutomaticDeliveryRegistry{API: store, starter: store, repositories: []registry.GitHubRepository{{RepositoryID: int64(identity.RepositoryID), OwnerID: int64(identity.RepositoryOwnerID), DefaultBranch: "main"}}}
	flaky.failures.Store(failures)
	server.Registry = flaky
	server.OIDC = &replayRejectingBuildRecordOIDC{identity: identity, seen: map[string]bool{}}
	return server, store, project.ID, identity
}
func testBuildRecordIdentity() githuboidc.VerifiedIdentity {
	return githuboidc.VerifiedIdentity{Issuer: githuboidc.GitHubIssuer, Subject: "repo:huutawn/opsi:ref:refs/heads/developer", Repository: "huutawn/opsi", RepositoryID: 7, RepositoryOwner: "huutawn", RepositoryOwnerID: 8, Ref: "refs/heads/developer", SHA: strings.Repeat("a", 40), EventName: "push", Workflow: "opsi-cd", WorkflowRef: "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", RunID: 99, RunAttempt: 1}
}
func testBuildRecordSubmission(i githuboidc.VerifiedIdentity) buildrecordv1.Submission {
	return buildrecordv1.Submission{SchemaVersion: buildrecordv1.SchemaVersion, ServiceKey: "api", RepositoryID: i.RepositoryID, RepositoryOwnerID: i.RepositoryOwnerID, Ref: i.Ref, SHA: i.SHA, EventName: i.EventName, WorkflowRef: i.WorkflowRef, RunID: i.RunID, RunAttempt: i.RunAttempt, ConfigHash: strings.Repeat("b", 64), PlanHash: strings.Repeat("d", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi/api", OCIDigest: "sha256:" + strings.Repeat("c", 64), Status: "succeeded"}
}
func mustBuildRecordBody(t *testing.T, identity githuboidc.VerifiedIdentity) []byte {
	t.Helper()
	body, err := json.Marshal(testBuildRecordSubmission(identity))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func testBuildRecordIdentityRecord(projectID string) buildrecordv1.Record {
	identity := testBuildRecordIdentity()
	return buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-read", ProjectID: projectID, RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID, ActiveBindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", CreatedAt: time.Unix(1, 0).UTC(), Workload: buildrecordv1.WorkloadIdentity{Issuer: identity.Issuer, Subject: identity.Subject, RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID, Ref: identity.Ref, SHA: identity.SHA, EventName: identity.EventName, Workflow: identity.Workflow, WorkflowRef: identity.WorkflowRef, RunID: identity.RunID, RunAttempt: identity.RunAttempt}, Build: buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("b", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi/api", OCIDigest: "sha256:" + strings.Repeat("c", 64), Status: "succeeded"}}
}
func postBuildRecord(t *testing.T, baseURL string, body []byte, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/build-records", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
func readResponse(response *http.Response) string {
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return string(data)
}
func assertBuildRecordAPIError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	raw := readResponse(response)
	if response.StatusCode != status || !strings.Contains(raw, code) {
		t.Fatalf("status=%d body=%s", response.StatusCode, raw)
	}
}
