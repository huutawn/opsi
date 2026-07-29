package webhookrelay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestBootstrapWorkerLeaseAndLeaseBoundMutations(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	project, err := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	session, err := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
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
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.40", "root", "password", "", "boot-checkpoint", 22)
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
	if w := request(body(0, "", strings.Repeat("a", 64)), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body(0, "", strings.Repeat("a", 64)), map[string]string{"X-Bootstrap-Worker-Token": "wrong"}); w.Code != http.StatusUnauthorized {
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
	workerAuth := map[string]string{"X-Bootstrap-Worker-Token": "worker-secret", "X-Bootstrap-Worker-ID": "worker-1", "X-Bootstrap-Lease-Token": lease.LeaseToken}
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
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
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
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
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
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
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
	session, _ := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
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
