package webhookrelay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/bootstrapworker"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestBootstrapCommandClaimsReviewedSessionOnce(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "long-lived-worker-secret", PublicBaseURL: "https://cloud.example"})
	runner := t.TempDir() + "/opsi-bootstrap-worker"
	if err := os.WriteFile(runner, []byte("reviewed-runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	install := bootstrapInstallFixture()
	if err := server.SetBootstrapRunner(install, runner); err != nil {
		t.Fatal(err)
	}
	project, err := server.Registry.CreateProject("org-1", "Command", "command", "", "project-command")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions", bytes.NewBufferString(`{"role":"first_server","public_host":"203.0.113.80","auth_method":"command"}`))
	create.Header.Set("Idempotency-Key", "command-create")
	create.Header.Set("X-Request-ID", "req-command-create")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var session registry.BootstrapSession
	if err := json.NewDecoder(created.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.Status != registry.BootstrapWaiting || session.AuthMethod != "command" || !strings.Contains(session.BootstrapCommand, "OPSI_BOOTSTRAP_TOKEN=") || strings.Contains(session.BootstrapCommand, server.Config.BootstrapWorkerToken) {
		t.Fatalf("session=%+v command=%q", session, session.BootstrapCommand)
	}
	duplicateCreate := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions", bytes.NewBufferString(`{"role":"first_server","public_host":"203.0.113.80","auth_method":"command"}`))
	duplicateCreate.Header.Set("Idempotency-Key", "command-create")
	duplicateCreate.Header.Set("X-Request-ID", "req-command-create-duplicate")
	duplicateCreateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateCreateResponse, duplicateCreate)
	var duplicateSession registry.BootstrapSession
	if duplicateCreateResponse.Code != http.StatusCreated || json.NewDecoder(duplicateCreateResponse.Body).Decode(&duplicateSession) != nil || duplicateSession.ID != session.ID || duplicateSession.BootstrapCommand != session.BootstrapCommand {
		t.Fatalf("duplicate create status=%d session=%+v body=%s", duplicateCreateResponse.Code, duplicateSession, duplicateCreateResponse.Body.String())
	}
	token := strings.TrimSuffix(strings.SplitN(session.BootstrapCommand, "OPSI_BOOTSTRAP_TOKEN='", 2)[1], "' sh")
	installRequest := httptest.NewRequest(http.MethodGet, "/v1/bootstrap/install", nil)
	installResponse := httptest.NewRecorder()
	handler.ServeHTTP(installResponse, installRequest)
	if installResponse.Code != http.StatusOK || !strings.Contains(installResponse.Body.String(), server.bootstrapRunnerSHA256) || !strings.Contains(installResponse.Body.String(), "https://cloud.example/v1/bootstrap/claim") || strings.Contains(installResponse.Body.String(), token) {
		t.Fatalf("install status=%d body=%s", installResponse.Code, installResponse.Body.String())
	}
	runnerRequest := httptest.NewRequest(http.MethodGet, "/v1/bootstrap/runner/linux-amd64", nil)
	runnerResponse := httptest.NewRecorder()
	handler.ServeHTTP(runnerResponse, runnerRequest)
	if runnerResponse.Code != http.StatusOK || runnerResponse.Body.String() != "reviewed-runner" {
		t.Fatalf("runner status=%d body=%q", runnerResponse.Code, runnerResponse.Body.String())
	}
	pool := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-pool"}`))
	pool.Header.Set("X-Bootstrap-Worker-Token", server.Config.BootstrapWorkerToken)
	poolResponse := httptest.NewRecorder()
	handler.ServeHTTP(poolResponse, pool)
	if poolResponse.Code != http.StatusNoContent {
		t.Fatalf("pool leased command session status=%d body=%s", poolResponse.Code, poolResponse.Body.String())
	}
	claim := func(value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", bytes.NewBufferString(`{"worker_id":"target-command"}`))
		req.Header.Set("Authorization", "Bootstrap "+value)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := claim(session.ID + ".wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d body=%s", w.Code, w.Body.String())
	}
	claimed := claim(token)
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	var lease struct {
		LeaseToken string `json:"lease_token"`
		Bundle     struct {
			SessionID  string                        `json:"session_id"`
			ProjectID  string                        `json:"project_id"`
			Checkpoint registry.BootstrapCheckpoint  `json:"checkpoint"`
			Install    bootstrapworker.InstallConfig `json:"install"`
			SSH        struct {
				AuthMethod string `json:"auth_method"`
				Username   string `json:"username"`
				PrivateKey string `json:"private_key"`
				Password   string `json:"password"`
			} `json:"ssh"`
		} `json:"bundle"`
	}
	if err := json.NewDecoder(claimed.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.Bundle.SessionID != session.ID || lease.Bundle.ProjectID != project.ID || lease.Bundle.SSH.AuthMethod != "command" || lease.Bundle.SSH.Username != "root" || lease.Bundle.SSH.PrivateKey != "" || lease.Bundle.SSH.Password != "" || lease.Bundle.Install != install || lease.LeaseToken == "" {
		t.Fatalf("lease=%+v", lease)
	}
	if server.credentials.Len() != 0 {
		t.Fatal("one-time command token remained after claim")
	}
	if w := claim(token); w.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d body=%s", w.Code, w.Body.String())
	}
	progress := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/sessions/"+session.ID+"/progress", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"connecting","message":"connected"}`))
	progress.Header.Set("X-Bootstrap-Worker-ID", "target-command")
	progress.Header.Set("X-Bootstrap-Lease-Token", lease.LeaseToken)
	progressResponse := httptest.NewRecorder()
	handler.ServeHTTP(progressResponse, progress)
	if progressResponse.Code != http.StatusOK {
		t.Fatalf("lease-bound progress status=%d body=%s", progressResponse.Code, progressResponse.Body.String())
	}
	finish := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"failed","failure_code":"BOOTSTRAP_COMMAND_FAILED","message":"command stopped","retryable":true}`))
	finish.Header.Set("X-Bootstrap-Worker-ID", "target-command")
	finish.Header.Set("X-Bootstrap-Lease-Token", lease.LeaseToken)
	finishResponse := httptest.NewRecorder()
	handler.ServeHTTP(finishResponse, finish)
	failed, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	if finishResponse.Code != http.StatusOK || failed.Status != registry.BootstrapDeadLetter {
		t.Fatalf("finish status=%d session=%+v body=%s", finishResponse.Code, failed, finishResponse.Body.String())
	}
	retry := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions/"+session.ID+"/retry", bytes.NewBufferString(`{}`))
	retry.Header.Set("Idempotency-Key", "command-retry")
	retry.Header.Set("X-Request-ID", "req-command-retry")
	retriedResponse := httptest.NewRecorder()
	handler.ServeHTTP(retriedResponse, retry)
	var retried registry.BootstrapSession
	if retriedResponse.Code != http.StatusAccepted || json.NewDecoder(retriedResponse.Body).Decode(&retried) != nil || retried.Status != registry.BootstrapWaiting || retried.BootstrapCommand == "" || retried.BootstrapCommand == session.BootstrapCommand {
		t.Fatalf("retry status=%d session=%+v body=%s", retriedResponse.Code, retried, retriedResponse.Body.String())
	}
	duplicateRetry := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions/"+session.ID+"/retry", bytes.NewBufferString(`{}`))
	duplicateRetry.Header.Set("Idempotency-Key", "command-retry")
	duplicateRetry.Header.Set("X-Request-ID", "req-command-retry-duplicate")
	duplicateRetryResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateRetryResponse, duplicateRetry)
	var duplicate registry.BootstrapSession
	if duplicateRetryResponse.Code != http.StatusAccepted || json.NewDecoder(duplicateRetryResponse.Body).Decode(&duplicate) != nil || duplicate.BootstrapCommand != retried.BootstrapCommand {
		t.Fatalf("duplicate retry status=%d session=%+v body=%s", duplicateRetryResponse.Code, duplicate, duplicateRetryResponse.Body.String())
	}
	retryToken := strings.TrimSuffix(strings.SplitN(retried.BootstrapCommand, "OPSI_BOOTSTRAP_TOKEN='", 2)[1], "' sh")
	if w := claim(retryToken); w.Code != http.StatusOK {
		t.Fatalf("retried command claim status=%d body=%s", w.Code, w.Body.String())
	}
	audit, _ := server.Registry.ListAudit(project.ID)
	auditJSON, _ := json.Marshal(audit)
	if bytes.Contains(auditJSON, []byte(token)) || bytes.Contains(auditJSON, []byte(retryToken)) {
		t.Fatal("bootstrap command token leaked into audit events")
	}
}

func TestBootstrapCommandExpiresBeforeClaim(t *testing.T) {
	server := NewServer(Config{PublicBaseURL: "https://cloud.example"})
	now := time.Now().UTC()
	store := NewCredentialStore()
	store.now = func() time.Time { return now }
	server.SetSecurityStores(store, nil, nil)
	runner := t.TempDir() + "/opsi-bootstrap-worker"
	if err := os.WriteFile(runner, []byte("runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := server.SetBootstrapRunner(bootstrapInstallFixture(), runner); err != nil {
		t.Fatal(err)
	}
	project, _ := server.Registry.CreateProject("org-1", "Expired", "expired", "", "project-expired")
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.81", "root", "command", "", "boot-expired", 0, "")
	token := session.ID + ".btok-expired"
	store.Put(session.ID, BootstrapCredential{AuthMethod: "command", Username: "root", Token: []byte(token)}, time.Second)
	now = now.Add(time.Second)
	req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", bytes.NewBufferString(`{"worker_id":"target-expired"}`))
	req.Header.Set("Authorization", "Bootstrap "+token)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired claim status=%d body=%s", w.Code, w.Body.String())
	}
	stored, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	if stored.Status != registry.BootstrapWaiting || stored.LeaseOwner != "" {
		t.Fatalf("expired token mutated session: %+v", stored)
	}
}

func bootstrapInstallFixture() bootstrapworker.InstallConfig {
	return bootstrapworker.InstallConfig{AgentCloudURL: "https://cloud.example", K3sVersion: "v1.32.5+k3s1", K3sInstallerURL: "https://get.k3s.io", K3sInstallerSHA256: strings.Repeat("b", 64), AgentInstallURL: "https://downloads.example/opsi-agent", AgentInstallSHA256: strings.Repeat("a", 64), Production: true}
}

func TestBootstrapWorkerLeaseAndLeaseBoundMutations(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	project, err := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.10", 22)
	session, err := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22, probeID)
	if err != nil {
		t.Fatal(err)
	}
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-secret", time.Hour)
	handler := server.Handler()

	request := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	workerAuth := map[string]string{"X-Bootstrap-Worker-Token": "worker-secret"}
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/lease", `{}`, workerAuth); w.Code != http.StatusBadRequest {
		t.Fatalf("missing worker_id status=%d body=%s", w.Code, w.Body.String())
	}
	w := request(http.MethodPost, "/internal/bootstrap/sessions/lease", `{"worker_id":"worker-1"}`, workerAuth)
	if w.Code != http.StatusOK {
		t.Fatalf("lease status=%d body=%s", w.Code, w.Body.String())
	}
	var lease struct {
		LeaseToken string `json:"lease_token"`
		Bundle     struct {
			SessionID string `json:"session_id"`
		} `json:"bundle"`
	}
	if err := json.NewDecoder(w.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.LeaseToken == "" || lease.Bundle.SessionID != session.ID {
		t.Fatalf("invalid lease response: %+v", lease)
	}
	audit, err := server.Registry.ListAudit(project.ID)
	if err != nil || len(audit) == 0 || audit[len(audit)-1].Action != "BOOTSTRAP_LEASE_ACQUIRED" || audit[len(audit)-1].MetadataRedacted["worker_id"] != "worker-1" {
		t.Fatalf("lease audit=%+v err=%v", audit, err)
	}
	if _, ok := audit[len(audit)-1].MetadataRedacted["lease_token"]; ok {
		t.Fatal("lease audit contains raw token")
	}
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/lease", `{"worker_id":"worker-2"}`, workerAuth); w.Code != http.StatusNoContent {
		t.Fatalf("second lease status=%d body=%s", w.Code, w.Body.String())
	}
	mutationBody := `{"project_id":"` + project.ID + `","status":"connecting","message":"connecting"}`
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", mutationBody, workerAuth); w.Code != http.StatusForbidden {
		t.Fatalf("progress without lease status=%d body=%s", w.Code, w.Body.String())
	}
	wrongOwner := map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Worker-ID": "worker-2", "X-Bootstrap-Lease-Token": lease.LeaseToken}
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", mutationBody, wrongOwner); w.Code != http.StatusForbidden {
		t.Fatalf("wrong owner status=%d body=%s", w.Code, w.Body.String())
	}
	owner := map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Worker-ID": "worker-1", "X-Bootstrap-Lease-Token": lease.LeaseToken}
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", mutationBody, owner); w.Code != http.StatusOK {
		t.Fatalf("valid progress status=%d body=%s", w.Code, w.Body.String())
	}
	finishBody := `{"project_id":"` + project.ID + `","status":"completed","message":"done"}`
	if w := request(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", finishBody, owner); w.Code != http.StatusOK {
		t.Fatalf("valid finish status=%d body=%s", w.Code, w.Body.String())
	}
	stored, err := server.Registry.GetBootstrapSession(project.ID, session.ID)
	if err != nil || stored.LeaseTokenHash != "" || stored.LeaseExpiresAt != nil || stored.LeaseOwner != "" {
		t.Fatalf("terminal lease state=%+v err=%v", stored, err)
	}
}

func TestBootstrapWorkerCheckpointAPIAndLeaseResponse(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	project, _ := server.Registry.CreateProject("org-1", "Checkpoint", "checkpoint", "", "project-checkpoint")
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.40", 22)
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.40", "root", "password", "", "boot-checkpoint", 22, probeID)
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-secret", time.Hour)
	handler := server.Handler()
	request := func(body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/checkpoint", bytes.NewBufferString(body))
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	body := func(index int, last, fingerprint string) string {
		return `{"project_id":"` + project.ID + `","schema_version":1,"plan_version":"first-server-v1","plan_fingerprint":"` + fingerprint + `","next_step_index":` + fmt.Sprint(index) + `,"last_completed_step":"` + last + `"}`
	}
	if w := request(body(0, "", strings.Repeat("a", 64)), nil); w.Code != http.StatusConflict {
		t.Fatalf("missing token status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body(0, "", strings.Repeat("a", 64)), map[string]string{"X-Bootstrap-Worker-Token": "wrong"}); w.Code != http.StatusConflict {
		t.Fatalf("invalid token status=%d body=%s", w.Code, w.Body.String())
	}
	leaseReq := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-1"}`))
	leaseReq.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	leaseResp := httptest.NewRecorder()
	handler.ServeHTTP(leaseResp, leaseReq)
	var lease struct {
		LeaseToken     string    `json:"lease_token"`
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
		Bundle         struct {
			Checkpoint registry.BootstrapCheckpoint `json:"checkpoint"`
		} `json:"bundle"`
	}
	if leaseResp.Code != http.StatusOK || json.NewDecoder(leaseResp.Body).Decode(&lease) != nil {
		t.Fatalf("lease status=%d body=%s", leaseResp.Code, leaseResp.Body.String())
	}
	if lease.Bundle.Checkpoint.SchemaVersion != 0 {
		t.Fatalf("fresh checkpoint=%+v", lease.Bundle.Checkpoint)
	}
	workerAuth := map[string]string{"X-Bootstrap-Worker-ID": "worker-1", "X-Bootstrap-Lease-Token": lease.LeaseToken}
	if w := request(body(0, "", strings.Repeat("a", 64)), map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Lease-Token": lease.LeaseToken}); w.Code != http.StatusForbidden {
		t.Fatalf("missing worker id status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body(0, "", strings.Repeat("a", 64)), map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Worker-ID": "worker-1", "X-Bootstrap-Lease-Token": "wrong"}); w.Code != http.StatusForbidden {
		t.Fatalf("wrong lease token status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body(0, "", "INVALID"), workerAuth); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid fingerprint status=%d body=%s", w.Code, w.Body.String())
	}
	initialized := request(body(0, "", strings.Repeat("a", 64)), workerAuth)
	if initialized.Code != http.StatusOK || strings.Contains(initialized.Body.String(), "command") || strings.Contains(initialized.Body.String(), "ssh-secret") || strings.Contains(initialized.Body.String(), "areg-secret") {
		t.Fatalf("initialization status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	advanced := request(body(1, "preflight", strings.Repeat("a", 64)), workerAuth)
	if advanced.Code != http.StatusOK {
		t.Fatalf("advance status=%d body=%s", advanced.Code, advanced.Body.String())
	}
	events, _ := server.Registry.BootstrapEvents(project.ID, session.ID)
	replayed := request(body(1, "preflight", strings.Repeat("a", 64)), workerAuth)
	eventsAfterReplay, _ := server.Registry.BootstrapEvents(project.ID, session.ID)
	if replayed.Code != http.StatusOK || len(eventsAfterReplay) != len(events) {
		t.Fatalf("replay status=%d events=%d/%d", replayed.Code, len(events), len(eventsAfterReplay))
	}
	if w := request(body(0, "", strings.Repeat("a", 64)), workerAuth); w.Code != http.StatusConflict {
		t.Fatalf("regression status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body(2, "install_k3s", strings.Repeat("b", 64)), workerAuth); w.Code != http.StatusConflict {
		t.Fatalf("plan mismatch status=%d body=%s", w.Code, w.Body.String())
	}
	finish := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"failed","failure_code":"BOOTSTRAP_CLOUD_TEMPORARY","message":"temporary","retryable":true}`))
	for key, value := range workerAuth {
		finish.Header.Set(key, value)
	}
	finishResp := httptest.NewRecorder()
	handler.ServeHTTP(finishResp, finish)
	retrying, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	now = *retrying.NextAttemptAt
	leaseReq = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-2"}`))
	leaseReq.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	leaseResp = httptest.NewRecorder()
	handler.ServeHTTP(leaseResp, leaseReq)
	var resumed struct {
		LeaseToken string `json:"lease_token"`
		Bundle     struct {
			Checkpoint registry.BootstrapCheckpoint `json:"checkpoint"`
		} `json:"bundle"`
	}
	if leaseResp.Code != http.StatusOK || json.NewDecoder(leaseResp.Body).Decode(&resumed) != nil || resumed.Bundle.Checkpoint.NextStepIndex != 1 {
		t.Fatalf("resumed lease status=%d body=%s checkpoint=%+v", leaseResp.Code, leaseResp.Body.String(), resumed.Bundle.Checkpoint)
	}
	now = now.Add(bootstrapLeaseDuration + time.Nanosecond)
	expiredAuth := map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Worker-ID": "worker-2", "X-Bootstrap-Lease-Token": resumed.LeaseToken}
	if w := request(body(2, "install_k3s", strings.Repeat("a", 64)), expiredAuth); w.Code != http.StatusGone {
		t.Fatalf("expired lease status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestConcurrentBootstrapLeaseRequestsReceiveOneSession(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	project, _ := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.10", 22)
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22, probeID)
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-secret", time.Hour)
	handler := server.Handler()
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, workerID := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"`+workerID+`"}`))
			req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			codes <- w.Code
		}(workerID)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusNoContent] != 1 {
		t.Fatalf("concurrent lease statuses=%v", counts)
	}
}

func TestBootstrapLeaseHeartbeatEndpointExtendsLeaseAndRejectsStaleLease(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	project, _ := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.10", 22)
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22, probeID)
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-initial", time.Hour)
	handler := server.Handler()

	leaseReq := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-1"}`))
	leaseReq.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	leaseResp := httptest.NewRecorder()
	handler.ServeHTTP(leaseResp, leaseReq)
	var lease struct {
		LeaseToken     string    `json:"lease_token"`
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	if leaseResp.Code != http.StatusOK || json.NewDecoder(leaseResp.Body).Decode(&lease) != nil {
		t.Fatalf("lease status=%d body=%s", leaseResp.Code, leaseResp.Body.String())
	}
	now = now.Add(20 * time.Second)
	heartbeat := func(workerID, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/lease-heartbeat", bytes.NewBufferString(`{"project_id":"`+project.ID+`"}`))
		req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
		req.Header.Set("X-Bootstrap-Worker-ID", workerID)
		req.Header.Set("X-Bootstrap-Lease-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	w := heartbeat("worker-1", lease.LeaseToken)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	stored, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	if stored.AttemptCount != 1 || stored.LeaseHeartbeatAt == nil || stored.LeaseExpiresAt == nil || !stored.LeaseExpiresAt.After(lease.LeaseExpiresAt) {
		t.Fatalf("stored=%+v", stored)
	}
	if w := heartbeat("worker-2", lease.LeaseToken); w.Code != http.StatusForbidden {
		t.Fatalf("wrong owner status=%d body=%s", w.Code, w.Body.String())
	}
	if w := heartbeat("worker-1", "wrong"); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token status=%d body=%s", w.Code, w.Body.String())
	}
	now = stored.LeaseExpiresAt.Add(time.Nanosecond)
	if w := heartbeat("worker-1", lease.LeaseToken); w.Code != http.StatusGone {
		t.Fatalf("expired heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := server.Registry.RecoverExpiredBootstrapLeases(now); err != nil {
		t.Fatal(err)
	}
	if w := heartbeat("worker-1", lease.LeaseToken); w.Code != http.StatusConflict {
		t.Fatalf("inactive heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBootstrapCredentialAndRegistrationTokenSurviveRetryAttempt(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	project, _ := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.10", 22)
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22, probeID)
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-initial", time.Hour)
	handler := server.Handler()

	lease := func() struct {
		Bundle struct {
			AgentRegistrationToken string `json:"agent_registration_token"`
			SSH                    struct {
				Password string `json:"password"`
			} `json:"ssh"`
		} `json:"bundle"`
		LeaseToken string `json:"lease_token"`
	} {
		req := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-1"}`))
		req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("lease status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			Bundle struct {
				AgentRegistrationToken string `json:"agent_registration_token"`
				SSH                    struct {
					Password string `json:"password"`
				} `json:"ssh"`
			} `json:"bundle"`
			LeaseToken string `json:"lease_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := lease()
	if first.Bundle.SSH.Password != "ssh-secret" || server.credentials.Len() != 1 {
		t.Fatalf("credential was destructively consumed: bundle=%+v len=%d", first.Bundle, server.credentials.Len())
	}
	if _, ok := server.registrations.Exchange(first.Bundle.AgentRegistrationToken); !ok {
		t.Fatal("first-attempt registration token was not usable")
	}
	finish := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"failed","failure_code":"BOOTSTRAP_CONNECT_FAILED","message":"temporary timeout","retryable":true}`))
	finish.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	finish.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	finish.Header.Set("X-Bootstrap-Lease-Token", first.LeaseToken)
	finishResp := httptest.NewRecorder()
	handler.ServeHTTP(finishResp, finish)
	if finishResp.Code != http.StatusOK {
		t.Fatalf("finish status=%d body=%s", finishResp.Code, finishResp.Body.String())
	}
	retrying, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	now = retrying.NextAttemptAt.Add(time.Nanosecond)
	second := lease()
	if second.Bundle.SSH.Password != "ssh-secret" || second.Bundle.AgentRegistrationToken == first.Bundle.AgentRegistrationToken {
		t.Fatalf("retry bundle=%+v first_token=%q", second.Bundle, first.Bundle.AgentRegistrationToken)
	}
	if _, ok := server.registrations.Exchange(first.Bundle.AgentRegistrationToken); ok {
		t.Fatal("registration token from the prior attempt remained valid")
	}
	if _, ok := server.registrations.Exchange(second.Bundle.AgentRegistrationToken); !ok {
		t.Fatal("registration token for the retry attempt was not usable")
	}
	complete := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"completed"}`))
	complete.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	complete.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	complete.Header.Set("X-Bootstrap-Lease-Token", second.LeaseToken)
	completeResp := httptest.NewRecorder()
	handler.ServeHTTP(completeResp, complete)
	if completeResp.Code != http.StatusOK || server.credentials.Len() != 0 {
		t.Fatalf("completion status=%d credential_len=%d body=%s", completeResp.Code, server.credentials.Len(), completeResp.Body.String())
	}
}

func TestBootstrapPermanentFailureDeadLetterCleansSecrets(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	project, _ := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	probeID := seedRelayTestProbe(t, server, project.ID, "203.0.113.10", 22)
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22, probeID)
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	server.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, "areg-initial", time.Hour)
	handler := server.Handler()
	leaseReq := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-1"}`))
	leaseReq.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	leaseResp := httptest.NewRecorder()
	handler.ServeHTTP(leaseResp, leaseReq)
	var lease struct {
		LeaseToken string `json:"lease_token"`
	}
	if leaseResp.Code != http.StatusOK || json.NewDecoder(leaseResp.Body).Decode(&lease) != nil {
		t.Fatalf("lease status=%d body=%s", leaseResp.Code, leaseResp.Body.String())
	}
	finish := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+project.ID+`","status":"failed","failure_code":"TARGET_OS_UNSUPPORTED","message":"unsupported target","retryable":false}`))
	finish.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	finish.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	finish.Header.Set("X-Bootstrap-Lease-Token", lease.LeaseToken)
	finishResp := httptest.NewRecorder()
	handler.ServeHTTP(finishResp, finish)
	stored, _ := server.Registry.GetBootstrapSession(project.ID, session.ID)
	if finishResp.Code != http.StatusOK || stored.Status != registry.BootstrapDeadLetter || server.credentials.Len() != 0 {
		t.Fatalf("finish=%d stored=%+v credential_len=%d body=%s", finishResp.Code, stored, server.credentials.Len(), finishResp.Body.String())
	}
	if _, ok := server.registrations.GetForBootstrapLease(session.ID); ok {
		t.Fatal("registration token remained after dead-letter cleanup")
	}
}
