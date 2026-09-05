package webhookrelay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/sshprobe"
	"golang.org/x/crypto/ssh"
)

func setupTestRelayServer(t *testing.T) (*Server, registry.Project, map[string]string) {
	t.Helper()
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }

	proj, err := server.Registry.CreateProject("org-1", "Probe API Project", "probe-api-proj", "owner-user", "key-proj")
	if err != nil {
		t.Fatal(err)
	}

	ownerHash, _ := auth.HashPAT("owner_pat")
	adminHash, _ := auth.HashPAT("admin_pat")
	devHash, _ := auth.HashPAT("developer_pat")
	viewerHash, _ := auth.HashPAT("viewer_pat")

	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner-user", OrgID: "org-1", ProjectID: proj.ID, Role: "Owner", Hash: ownerHash},
		{UserID: "admin-user", OrgID: "org-1", ProjectID: proj.ID, Role: "Admin", Hash: adminHash},
		{UserID: "dev-user", OrgID: "org-1", ProjectID: proj.ID, Role: "Developer", Hash: devHash},
		{UserID: "viewer-user", OrgID: "org-1", ProjectID: proj.ID, Role: "Viewer", Hash: viewerHash},
	}}}

	tokens := map[string]string{
		"owner":     "owner_pat",
		"admin":     "admin_pat",
		"developer": "developer_pat",
		"viewer":    "viewer_pat",
	}

	return server, proj, tokens
}

func startMockSSHListener(t *testing.T) (net.Listener, int, string, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _, _, _ = ssh.NewServerConn(c, config)
			}(conn)
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	fp := ssh.FingerprintSHA256(signer.PublicKey())
	pubKey := signer.PublicKey().Marshal()

	return listener, port, fp, string(pubKey)
}

func TestAPI_SSHHostKeyProbes_RBAC_And_Execution(t *testing.T) {
	server, proj, tokens := setupTestRelayServer(t)
	handler := server.Handler()

	listener, port, expectedFP, _ := startMockSSHListener(t)
	defer listener.Close()

	// Configure server probe service to allow test server on localhost
	server.SSHProbe = &sshprobe.Service{
		Timeout:         2 * time.Second,
		AllowPrivateIPs: true,
	}

	probeBody := `{"public_host":"127.0.0.1","ssh_port":` + strconv.Itoa(port) + `}`

	// 1. Viewer is forbidden
	reqViewer := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes", bytes.NewBufferString(probeBody))
	reqViewer.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	reqViewer.Header.Set("Idempotency-Key", "key-probe-viewer")
	wViewer := httptest.NewRecorder()
	handler.ServeHTTP(wViewer, reqViewer)
	if wViewer.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d: %s", wViewer.Code, wViewer.Body.String())
	}

	// 2. Developer can probe
	reqDev := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes", bytes.NewBufferString(probeBody))
	reqDev.Header.Set("Authorization", "Bearer "+tokens["developer"])
	reqDev.Header.Set("Idempotency-Key", "key-probe-dev")
	wDev := httptest.NewRecorder()
	handler.ServeHTTP(wDev, reqDev)
	if wDev.Code != http.StatusCreated {
		t.Fatalf("expected 201 for developer, got %d: %s", wDev.Code, wDev.Body.String())
	}

	var probeResp map[string]any
	if err := json.NewDecoder(wDev.Body).Decode(&probeResp); err != nil {
		t.Fatal(err)
	}
	probeID, _ := probeResp["probe_id"].(string)
	if probeID == "" {
		t.Fatal("expected probe_id in response")
	}
	if probeResp["fingerprint"] != expectedFP {
		t.Fatalf("expected fingerprint %s, got %v", expectedFP, probeResp["fingerprint"])
	}
	if probeResp["trust_state"] != "first_seen" {
		t.Fatalf("expected first_seen trust_state, got %v", probeResp["trust_state"])
	}

	// 3. Viewer CAN read probe details
	reqGet := httptest.NewRequest(http.MethodGet, "/api/projects/"+proj.ID+"/ssh-host-key-probes/"+probeID, nil)
	reqGet.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	wGet := httptest.NewRecorder()
	handler.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for viewer GET probe, got %d: %s", wGet.Code, wGet.Body.String())
	}

	// 4. Viewer CAN read trusts list
	reqTrusts := httptest.NewRequest(http.MethodGet, "/api/projects/"+proj.ID+"/ssh-host-key-trusts", nil)
	reqTrusts.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	wTrusts := httptest.NewRecorder()
	handler.ServeHTTP(wTrusts, reqTrusts)
	if wTrusts.Code != http.StatusOK {
		t.Fatalf("expected 200 for viewer GET trusts, got %d: %s", wTrusts.Code, wTrusts.Body.String())
	}
}

func TestAPI_BootstrapCreate_ProbeValidation(t *testing.T) {
	server, proj, tokens := setupTestRelayServer(t)
	handler := server.Handler()

	listener, port, _, _ := startMockSSHListener(t)
	defer listener.Close()

	server.SSHProbe = &sshprobe.Service{
		Timeout:         2 * time.Second,
		AllowPrivateIPs: true,
	}

	// Probe host
	probeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes", bytes.NewBufferString(`{"public_host":"127.0.0.1","ssh_port":`+strconv.Itoa(port)+`}`))
	probeReq.Header.Set("Authorization", "Bearer "+tokens["developer"])
	probeReq.Header.Set("Idempotency-Key", "probe-key")
	wProbe := httptest.NewRecorder()
	handler.ServeHTTP(wProbe, probeReq)
	var probeResp map[string]any
	_ = json.NewDecoder(wProbe.Body).Decode(&probeResp)
	probeID := probeResp["probe_id"].(string)

	// 1. SSH bootstrap without probe ID must be rejected
	noProbeBody := `{"public_host":"127.0.0.1","ssh_port":` + strconv.Itoa(port) + `,"auth_method":"password","ssh_username":"root","ssh_password":"secret"}`
	reqNoProbe := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/bootstrap-sessions", bytes.NewBufferString(noProbeBody))
	reqNoProbe.Header.Set("Authorization", "Bearer "+tokens["admin"])
	reqNoProbe.Header.Set("Idempotency-Key", "boot-no-probe")
	wNoProbe := httptest.NewRecorder()
	handler.ServeHTTP(wNoProbe, reqNoProbe)
	if wNoProbe.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for SSH bootstrap without probe, got %d: %s", wNoProbe.Code, wNoProbe.Body.String())
	}

	// 2. Command bootstrap WITH probe ID must be rejected
	cmdWithProbeBody := `{"public_host":"127.0.0.1","auth_method":"command","ssh_host_key_probe_id":"` + probeID + `"}`
	reqCmdWithProbe := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/bootstrap-sessions", bytes.NewBufferString(cmdWithProbeBody))
	reqCmdWithProbe.Header.Set("Authorization", "Bearer "+tokens["admin"])
	reqCmdWithProbe.Header.Set("Idempotency-Key", "boot-cmd-probe")
	wCmdWithProbe := httptest.NewRecorder()
	handler.ServeHTTP(wCmdWithProbe, reqCmdWithProbe)
	if wCmdWithProbe.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for command bootstrap with probe ID, got %d: %s", wCmdWithProbe.Code, wCmdWithProbe.Body.String())
	}

	// 3. SSH bootstrap WITH valid probe ID succeeds
	validBody := `{"public_host":"127.0.0.1","ssh_port":` + strconv.Itoa(port) + `,"auth_method":"password","ssh_username":"root","ssh_password":"secret","ssh_host_key_probe_id":"` + probeID + `"}`
	reqValid := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/bootstrap-sessions", bytes.NewBufferString(validBody))
	reqValid.Header.Set("Authorization", "Bearer "+tokens["admin"])
	reqValid.Header.Set("Idempotency-Key", "boot-valid")
	wValid := httptest.NewRecorder()
	handler.ServeHTTP(wValid, reqValid)
	if wValid.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid bootstrap, got %d: %s", wValid.Code, wValid.Body.String())
	}
}

func TestAPI_ConfirmRotation_And_ResumeSession(t *testing.T) {
	server, proj, tokens := setupTestRelayServer(t)
	handler := server.Handler()

	listener1, port, _, _ := startMockSSHListener(t)
	defer listener1.Close()

	server.SSHProbe = &sshprobe.Service{
		Timeout:         2 * time.Second,
		AllowPrivateIPs: true,
	}

	// 1. Initial probe and bootstrap
	probe1Req := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes", bytes.NewBufferString(`{"public_host":"127.0.0.1","ssh_port":`+strconv.Itoa(port)+`}`))
	probe1Req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	probe1Req.Header.Set("Idempotency-Key", "probe1-key")
	wProbe1 := httptest.NewRecorder()
	handler.ServeHTTP(wProbe1, probe1Req)
	var p1 map[string]any
	_ = json.NewDecoder(wProbe1.Body).Decode(&p1)
	probe1ID := p1["probe_id"].(string)

	bootReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/bootstrap-sessions", bytes.NewBufferString(`{"public_host":"127.0.0.1","ssh_port":`+strconv.Itoa(port)+`,"auth_method":"password","ssh_username":"root","ssh_password":"secret","ssh_host_key_probe_id":"`+probe1ID+`"}`))
	bootReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	bootReq.Header.Set("Idempotency-Key", "boot1-key")
	wBoot := httptest.NewRecorder()
	handler.ServeHTTP(wBoot, bootReq)
	var session registry.BootstrapSession
	_ = json.NewDecoder(wBoot.Body).Decode(&session)

	// 2. Lease to worker, worker reports waiting_host_key_confirmation (key rotated!)
	leaseReq := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewBufferString(`{"worker_id":"worker-rot"}`))
	leaseReq.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	wLease := httptest.NewRecorder()
	handler.ServeHTTP(wLease, leaseReq)
	var leaseResp map[string]any
	_ = json.NewDecoder(wLease.Body).Decode(&leaseResp)
	leaseToken := leaseResp["lease_token"].(string)

	// Start new listener with new key (server 2)
	listener2, _, newFP, _ := startMockSSHListener(t)
	defer listener2.Close()

	finishReq := httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewBufferString(`{"project_id":"`+proj.ID+`","status":"waiting_host_key_confirmation","failure_code":"SSH_HOST_KEY_MISMATCH","message":"host key mismatch","observed_algorithm":"ssh-ed25519","observed_fingerprint":"`+newFP+`"}`))
	finishReq.Header.Set("X-Bootstrap-Worker-ID", "worker-rot")
	finishReq.Header.Set("X-Bootstrap-Lease-Token", leaseToken)
	wFinish := httptest.NewRecorder()
	handler.ServeHTTP(wFinish, finishReq)
	if wFinish.Code != http.StatusOK {
		t.Fatalf("finish failed: %d: %s", wFinish.Code, wFinish.Body.String())
	}

	// 3. Find the changed observation created by the worker finish
	trusts, _ := server.Registry.ListSSHHostKeyTrusts(proj.ID)
	if len(trusts) == 0 {
		t.Fatal("expected active trust")
	}

	// Create probe for new key
	obs2, err := server.Registry.CreateSSHHostKeyObservation(proj.ID, "127.0.0.1", port, "127.0.0.1", "ssh-ed25519", "new-pubkey", newFP, "dev-user", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if obs2.TrustState != "changed" {
		t.Fatalf("expected changed trust_state, got %s", obs2.TrustState)
	}

	// 4. Confirm rotation via API (Developer allowed, Viewer rejected)
	confViewer := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes/"+obs2.ID+"/confirm", bytes.NewBufferString(`{"fingerprint":"`+newFP+`"}`))
	confViewer.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	confViewer.Header.Set("Idempotency-Key", "conf-v")
	wConfViewer := httptest.NewRecorder()
	handler.ServeHTTP(wConfViewer, confViewer)
	if wConfViewer.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer confirm, got %d", wConfViewer.Code)
	}

	confDev := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/ssh-host-key-probes/"+obs2.ID+"/confirm", bytes.NewBufferString(`{"fingerprint":"`+newFP+`"}`))
	confDev.Header.Set("Authorization", "Bearer "+tokens["developer"])
	confDev.Header.Set("Idempotency-Key", "conf-dev")
	wConfDev := httptest.NewRecorder()
	handler.ServeHTTP(wConfDev, confDev)
	if wConfDev.Code != http.StatusOK {
		t.Fatalf("expected 200 for developer confirm, got %d: %s", wConfDev.Code, wConfDev.Body.String())
	}

	// 5. Resume session via API (Developer allowed)
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/bootstrap-sessions/"+session.ID+"/resume", bytes.NewBufferString(`{"ssh_host_key_probe_id":"`+obs2.ID+`","auth_method":"password","ssh_username":"root","ssh_password":"new-password"}`))
	resumeReq.Header.Set("Authorization", "Bearer "+tokens["developer"])
	resumeReq.Header.Set("Idempotency-Key", "resume-key")
	wResume := httptest.NewRecorder()
	handler.ServeHTTP(wResume, resumeReq)
	if wResume.Code != http.StatusOK {
		t.Fatalf("expected 200 for resume session, got %d: %s", wResume.Code, wResume.Body.String())
	}

	var resumedSession registry.BootstrapSession
	_ = json.NewDecoder(wResume.Body).Decode(&resumedSession)
	if resumedSession.Status != "pending" {
		t.Fatalf("expected resumed status pending, got %s", resumedSession.Status)
	}
}
