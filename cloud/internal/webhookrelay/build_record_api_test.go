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
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

type buildRecordOIDCFixture struct{ identity githuboidc.VerifiedIdentity }

func (f buildRecordOIDCFixture) Verify(_ context.Context, token string) (githuboidc.VerifiedIdentity, error) {
	if token != "synthetic-oidc-token" {
		return githuboidc.VerifiedIdentity{}, errors.New("invalid")
	}
	return f.identity, nil
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
func testBuildRecordIdentity() githuboidc.VerifiedIdentity {
	return githuboidc.VerifiedIdentity{Issuer: githuboidc.GitHubIssuer, Subject: "repo:huutawn/opsi:ref:refs/heads/developer", Repository: "huutawn/opsi", RepositoryID: 7, RepositoryOwner: "huutawn", RepositoryOwnerID: 8, Ref: "refs/heads/developer", SHA: strings.Repeat("a", 40), EventName: "push", Workflow: "opsi-cd", WorkflowRef: "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", RunID: 99, RunAttempt: 1}
}
func testBuildRecordSubmission(i githuboidc.VerifiedIdentity) buildrecordv1.Submission {
	return buildrecordv1.Submission{SchemaVersion: buildrecordv1.SchemaVersion, ServiceKey: "api", RepositoryID: i.RepositoryID, RepositoryOwnerID: i.RepositoryOwnerID, Ref: i.Ref, SHA: i.SHA, EventName: i.EventName, WorkflowRef: i.WorkflowRef, RunID: i.RunID, RunAttempt: i.RunAttempt, ConfigHash: strings.Repeat("b", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi/api", OCIDigest: "sha256:" + strings.Repeat("c", 64), Status: "succeeded"}
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
