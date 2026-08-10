package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
)

type buildJobAPISource struct{ source buildjob.ApplicationSource }

func (s buildJobAPISource) ResolveBuildJobSource(context.Context, string, string) (buildjob.ApplicationSource, error) {
	return s.source, nil
}

type buildJobAPIRepository struct{ sha string }

func (r buildJobAPIRepository) ResolveCommit(context.Context, int64, string, string) (string, error) {
	return r.sha, nil
}

func (buildJobAPIRepository) RepositoryFileExists(context.Context, int64, string, string, string) (bool, error) {
	return true, nil
}

func TestBuildJobAPICreateGetListIdempotencyAndNoBuildRecordForgery(t *testing.T) {
	trustedHash, err := auth.HashPAT("owner-pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{UserID: "owner", OrgID: "org-1", ProjectID: "project-1", Role: "owner", Hash: trustedHash, ExpiresAt: time.Now().Add(time.Hour)}}}}
	ids := 0
	server.BuildJobs = buildjob.Service{
		Store:      buildjob.NewMemoryStore(),
		Sources:    buildJobAPISource{source: buildjob.ApplicationSource{ProjectID: "project-1", EnvironmentID: "environment-1", ApplicationID: "application-1", BindingID: "binding-1", BindingUpdatedAt: time.Unix(50, 0).UTC(), InstallationID: 10, RepositoryID: 20, RepositoryOwnerID: 30, RepositoryFullName: "owner/repository", SelectedRef: "main", ApplicationRoot: ".", BuildContext: ".", BuildStrategy: buildjob.StrategyAuto}},
		Repository: buildJobAPIRepository{sha: strings.Repeat("a", 40)},
		Now:        func() time.Time { return time.Unix(100, 0).UTC() },
		NewID:      func() (string, error) { ids++; return fmt.Sprintf("job-%d", ids), nil },
	}
	server.BuildRecords = buildrecord.Service{Store: buildrecord.NewMemoryStore()}
	handler := server.Handler()

	injected := buildJobAPIRequest(handler, http.MethodPost, "/v1/projects/project-1/applications/application-1/build-jobs", `{"resolved_commit_sha":"`+strings.Repeat("b", 40)+`"}`, "owner-pat", "injected")
	if injected.Code != http.StatusBadRequest || !strings.Contains(injected.Body.String(), "BUILD_JOB_INTENT_INVALID") {
		t.Fatalf("injected status=%d body=%s", injected.Code, injected.Body.String())
	}

	createdResponse := buildJobAPIRequest(handler, http.MethodPost, "/v1/projects/project-1/applications/application-1/build-jobs", `{}`, "owner-pat", "same-key")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created buildjob.Job
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || created.Source.ResolvedCommitSHA != strings.Repeat("a", 40) || created.Status != buildjob.StatusReady {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replayedResponse := buildJobAPIRequest(handler, http.MethodPost, "/v1/projects/project-1/applications/application-1/build-jobs", `{}`, "owner-pat", "same-key")
	var replayed buildjob.Job
	if replayedResponse.Code != http.StatusOK || json.Unmarshal(replayedResponse.Body.Bytes(), &replayed) != nil || replayed.ID != created.ID {
		t.Fatalf("replay status=%d body=%s", replayedResponse.Code, replayedResponse.Body.String())
	}
	distinctResponse := buildJobAPIRequest(handler, http.MethodPost, "/v1/projects/project-1/applications/application-1/build-jobs", `{}`, "owner-pat", "different-key")
	var distinct buildjob.Job
	if distinctResponse.Code != http.StatusCreated || json.Unmarshal(distinctResponse.Body.Bytes(), &distinct) != nil || distinct.ID == created.ID {
		t.Fatalf("distinct status=%d body=%s", distinctResponse.Code, distinctResponse.Body.String())
	}

	read := buildJobAPIRequest(handler, http.MethodGet, "/v1/projects/project-1/applications/application-1/build-jobs/"+created.ID, "", "owner-pat", "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"resolved_commit_sha":"`+strings.Repeat("a", 40)+`"`) {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	list := buildJobAPIRequest(handler, http.MethodGet, "/v1/projects/project-1/applications/application-1/build-jobs?status=ready", "", "owner-pat", "")
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), `"id":"job-`) != 2 {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	records, err := server.BuildRecords.List(context.Background(), "project-1", buildrecord.ListFilter{Limit: 50})
	if err != nil || len(records.Records) != 0 {
		t.Fatalf("BuildJob forged BuildRecords: records=%+v err=%v", records, err)
	}
}

func buildJobAPIRequest(handler http.Handler, method, target, body, token, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
