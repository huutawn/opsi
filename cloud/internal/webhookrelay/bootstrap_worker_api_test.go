package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
	if err != nil || stored.LeaseTokenHash != "" || stored.LeaseExpiresAt != nil || stored.LeaseOwner != "worker-1" {
		t.Fatalf("terminal lease state=%+v err=%v", stored, err)
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
