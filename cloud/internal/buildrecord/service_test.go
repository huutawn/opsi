package buildrecord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

type testBindingResolver struct {
	binding Binding
	err     error
}

func (r testBindingResolver) ResolveBuildBinding(context.Context, uint64, string) (Binding, error) {
	return r.binding, r.err
}

func testIdentity() githuboidc.VerifiedIdentity {
	return githuboidc.VerifiedIdentity{Issuer: githuboidc.GitHubIssuer, Subject: "repo:huutawn/opsi:ref:refs/heads/developer", Repository: "huutawn/opsi", RepositoryID: 7, RepositoryOwner: "huutawn", RepositoryOwnerID: 8, Ref: "refs/heads/developer", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EventName: "push", Workflow: "opsi-cd", WorkflowRef: "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", RunID: 99, RunAttempt: 1}
}
func testSubmission(identity githuboidc.VerifiedIdentity) buildrecordv1.Submission {
	return buildrecordv1.Submission{SchemaVersion: buildrecordv1.SchemaVersion, ServiceKey: "api", RepositoryID: identity.RepositoryID, RepositoryOwnerID: identity.RepositoryOwnerID, Ref: identity.Ref, SHA: identity.SHA, EventName: identity.EventName, WorkflowRef: identity.WorkflowRef, RunID: identity.RunID, RunAttempt: identity.RunAttempt, ConfigHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi/api", OCIDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Status: "succeeded"}
}
func testService() Service {
	return Service{Store: NewMemoryStore(), Bindings: testBindingResolver{binding: Binding{ProjectID: "project-1", BindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 8}}, Policies: []githuboidc.WorkloadPolicy{{RepositoryID: 7, ServiceKey: "api", WorkflowRefs: []string{"huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer"}, Refs: []string{"refs/heads/developer"}, Events: []string{"push"}, OCIRepositories: []string{"ghcr.io/huutawn/opsi/api"}}}, Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewID: func() (string, error) { return "br-1", nil }}
}

func TestSubmitBindsClaimsAndReplaysExactly(t *testing.T) {
	service := testService()
	identity := testIdentity()
	request := testSubmission(identity)
	record, reused, err := service.Submit(context.Background(), identity, request)
	if err != nil || reused {
		t.Fatalf("first record=%+v reused=%v err=%v", record, reused, err)
	}
	replay, reused, err := service.Submit(context.Background(), identity, request)
	if err != nil || !reused || replay.ID != record.ID {
		t.Fatalf("replay=%+v reused=%v err=%v", replay, reused, err)
	}
	request.OCIDigest = "sha256:" + "d" + request.OCIDigest[len("sha256:")+1:]
	if _, _, err := service.Submit(context.Background(), identity, request); err == nil || err.(Error).Code != "BUILD_RECORD_CONFLICT" {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestSubmitRejectsClaimAndBindingMismatches(t *testing.T) {
	service := testService()
	identity := testIdentity()
	request := testSubmission(identity)
	request.SHA = "dddddddddddddddddddddddddddddddddddddddd"
	if _, _, err := service.Submit(context.Background(), identity, request); err == nil || err.(Error).Code != "BUILD_CLAIM_BODY_MISMATCH" {
		t.Fatalf("claim mismatch=%v", err)
	}
	request = testSubmission(identity)
	request.OCIDigest = "sha256:tag-only"
	if _, _, err := service.Submit(context.Background(), identity, request); err == nil || err.(Error).Code != "BUILD_ARTIFACT_INVALID" {
		t.Fatalf("digest mismatch=%v", err)
	}
	service.Bindings = testBindingResolver{binding: Binding{ProjectID: "other", BindingID: "binding-2", ServiceID: "service-2", ServiceKey: "api", RepositoryID: 7, RepositoryOwnerID: 9}}
	if _, _, err := service.Submit(context.Background(), identity, testSubmission(identity)); err == nil || err.(Error).Code != "BUILD_BINDING_MISMATCH" {
		t.Fatalf("binding mismatch=%v", err)
	}
}

func TestSubmitRequiresExplicitReusableWorkflowPolicy(t *testing.T) {
	service := testService()
	identity := testIdentity()
	identity.JobWorkflowRef = "huutawn/automation/.github/workflows/build.yml@refs/heads/main"
	request := testSubmission(identity)
	request.JobWorkflowRef = identity.JobWorkflowRef
	if _, _, err := service.Submit(context.Background(), identity, request); err == nil || err.(Error).Code != "BUILD_WORKLOAD_FORBIDDEN" {
		t.Fatalf("implicit reusable workflow err=%v", err)
	}
	service.Policies[0].JobWorkflowRefs = []string{identity.JobWorkflowRef}
	if _, _, err := service.Submit(context.Background(), identity, request); err != nil {
		t.Fatalf("explicit reusable workflow: %v", err)
	}
}

func TestSubmitRejectsMissingActiveBinding(t *testing.T) {
	service := testService()
	service.Bindings = testBindingResolver{err: errors.New("removed")}
	if _, _, err := service.Submit(context.Background(), testIdentity(), testSubmission(testIdentity())); err == nil || err.(Error).Code != "BUILD_BINDING_INVALID" {
		t.Fatalf("binding err=%v", err)
	}
}

func TestSubmitConcurrentDuplicateCreatesOneMemoryRecord(t *testing.T) {
	service := testService()
	identity := testIdentity()
	request := testSubmission(identity)
	var wait sync.WaitGroup
	results := make(chan buildrecordv1.Record, 8)
	reused := make(chan bool, 8)
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, wasReused, err := service.Submit(context.Background(), identity, request)
			results <- record
			reused <- wasReused
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(reused)
	close(errs)
	var id string
	count := 0
	for record := range results {
		if id == "" {
			id = record.ID
		}
		if record.ID != id {
			t.Fatalf("different records: %q/%q", id, record.ID)
		}
		count++
	}
	if count != 8 {
		t.Fatal("missing concurrent results")
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	createdCount, reusedCount := 0, 0
	for wasReused := range reused {
		if wasReused {
			reusedCount++
		} else {
			createdCount++
		}
	}
	if createdCount != 1 || reusedCount != 7 {
		t.Fatalf("created=%d reused=%d", createdCount, reusedCount)
	}
}

func TestListIsProjectScopedAndStable(t *testing.T) {
	service := testService()
	for i := 0; i < 2; i++ {
		identity := testIdentity()
		identity.RunID += uint64(i)
		request := testSubmission(identity)
		id := "br-" + string(rune('1'+i))
		service.NewID = func() (string, error) { return id, nil }
		if _, _, err := service.Submit(context.Background(), identity, request); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.List(context.Background(), "project-1", ListFilter{Limit: 1})
	if err != nil || len(result.Records) != 1 || result.NextCursor == "" {
		t.Fatalf("list=%+v err=%v", result, err)
	}
	next, err := service.List(context.Background(), "project-1", ListFilter{Limit: 1, Cursor: result.NextCursor})
	if err != nil || len(next.Records) != 1 || next.Records[0].ID == result.Records[0].ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	other, err := service.List(context.Background(), "other", ListFilter{Limit: 1})
	if err != nil || len(other.Records) != 0 {
		t.Fatalf("other=%+v err=%v", other, err)
	}
	if _, err := service.List(context.Background(), "project-1", ListFilter{Limit: 1, Cursor: strings.Repeat("a", 1025)}); err == nil || err.(Error).Code != "BUILD_RECORD_CURSOR_INVALID" {
		t.Fatalf("oversized cursor err=%v", err)
	}
}
