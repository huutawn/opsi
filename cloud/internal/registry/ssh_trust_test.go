package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func generateTestSSHKey(t *testing.T) (string, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubKey := base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	return pubKey, fingerprint
}

func TestSSHTrust_TOFU_And_Match(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	proj, err := service.CreateProject("org-1", "Project A", "proj-a", "user-1", "key-proj-a")
	if err != nil {
		t.Fatal(err)
	}

	pubKeyA, fpA := generateTestSSHKey(t)
	host := "203.0.113.10"
	port := 22

	// 1. First probe should be first_seen
	obs1, err := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKeyA, fpA, "user-1", now)
	if err != nil {
		t.Fatalf("CreateSSHHostKeyObservation failed: %v", err)
	}
	if obs1.TrustState != TrustStateFirstSeen {
		t.Fatalf("expected TrustStateFirstSeen, got %s", obs1.TrustState)
	}
	if obs1.Status != ObservationStatusPending {
		t.Fatalf("expected pending status, got %s", obs1.Status)
	}

	// 2. Create bootstrap session using the probe -> should atomically trust (TOFU)
	session, err := service.CreateBootstrapSession(proj.ID, "first_server", host, "root", "password", "user-1", "boot-1", port, obs1.ID)
	if err != nil {
		t.Fatalf("CreateBootstrapSession failed: %v", err)
	}
	if session.SSHHostKeyTrustID == "" {
		t.Fatal("expected session to have SSHHostKeyTrustID set")
	}
	if session.ResolvedIP != host {
		t.Fatalf("expected resolved IP %s, got %s", host, session.ResolvedIP)
	}

	// Verify active trust was created
	trust, err := service.GetActiveSSHHostKeyTrust(proj.ID, host, port)
	if err != nil {
		t.Fatalf("GetActiveSSHHostKeyTrust failed: %v", err)
	}
	if trust.ID != session.SSHHostKeyTrustID {
		t.Fatalf("trust ID mismatch: %s vs %s", trust.ID, session.SSHHostKeyTrustID)
	}
	if trust.Fingerprint != fpA {
		t.Fatalf("fingerprint mismatch: %s vs %s", trust.Fingerprint, fpA)
	}
	if trust.Status != TrustStatusActive {
		t.Fatalf("trust status: %s", trust.Status)
	}

	// 3. Second probe with identical key -> should be matched
	obs2, err := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKeyA, fpA, "user-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second observation failed: %v", err)
	}
	if obs2.TrustState != TrustStateMatched {
		t.Fatalf("expected TrustStateMatched, got %s", obs2.TrustState)
	}
	if obs2.PreviousFingerprint != "" {
		t.Fatalf("expected empty previous fingerprint, got %s", obs2.PreviousFingerprint)
	}
}

func TestSSHTrust_ProjectIsolation(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	projA, _ := service.CreateProject("org-1", "Project A", "proj-a", "user-1", "key-proj-a")
	projB, _ := service.CreateProject("org-1", "Project B", "proj-b", "user-1", "key-proj-b")

	pubKey, fp := generateTestSSHKey(t)
	host := "203.0.113.50"
	port := 22

	// Project A probes and bootstraps (pins key)
	obsA, _ := service.CreateSSHHostKeyObservation(projA.ID, host, port, host, ssh.KeyAlgoED25519, pubKey, fp, "user-1", now)
	_, err := service.CreateBootstrapSession(projA.ID, "first_server", host, "root", "password", "user-1", "boot-a", port, obsA.ID)
	if err != nil {
		t.Fatalf("Project A bootstrap failed: %v", err)
	}

	// Project B probes the EXACT SAME host and port -> must be first_seen for Project B!
	obsB, _ := service.CreateSSHHostKeyObservation(projB.ID, host, port, host, ssh.KeyAlgoED25519, pubKey, fp, "user-1", now)
	if obsB.TrustState != TrustStateFirstSeen {
		t.Fatalf("expected Project B to see first_seen (project isolation), got %s", obsB.TrustState)
	}

	// Project B cannot use Project A's probe ID
	_, err = service.CreateBootstrapSession(projB.ID, "first_server", host, "root", "password", "user-1", "boot-b-bad", port, obsA.ID)
	if err == nil {
		t.Fatal("expected error when Project B uses Project A's probe, got nil")
	}
}

func TestSSHTrust_KeyChange_And_Rotation(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	proj, _ := service.CreateProject("org-1", "Rotation Test", "proj-rot", "user-1", "key-proj-rot")
	host := "203.0.113.60"
	port := 22

	pubKey1, fp1 := generateTestSSHKey(t)
	pubKey2, fp2 := generateTestSSHKey(t)

	// Establish initial trust with Key 1
	obs1, _ := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKey1, fp1, "user-1", now)
	initSession, _ := service.CreateBootstrapSession(proj.ID, "first_server", host, "root", "password", "user-1", "boot-init", port, obs1.ID)
	service.mu.Lock()
	initSession.Status = "completed"
	service.bootstraps[initSession.ID] = initSession
	service.mu.Unlock()

	oldTrust, _ := service.GetActiveSSHHostKeyTrust(proj.ID, host, port)

	// Probe observes Key 2 (changed key!)
	obs2, err := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKey2, fp2, "user-1", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if obs2.TrustState != TrustStateChanged {
		t.Fatalf("expected TrustStateChanged, got %s", obs2.TrustState)
	}
	if obs2.PreviousFingerprint != fp1 {
		t.Fatalf("expected previous fingerprint %s, got %s", fp1, obs2.PreviousFingerprint)
	}

	// Attempting bootstrap with unconfirmed changed probe must fail
	_, err = service.CreateBootstrapSession(proj.ID, "first_server", host, "root", "password", "user-1", "boot-blocked", port, obs2.ID)
	if apiErrorCode(err) != "SSH_HOST_KEY_CONFIRMATION_REQUIRED" {
		t.Fatalf("expected SSH_HOST_KEY_CONFIRMATION_REQUIRED, got %v", err)
	}

	// Confirm rotation with wrong fingerprint must fail
	_, err = service.ConfirmSSHHostKeyRotation(proj.ID, obs2.ID, "user-1", "SHA256:WRONGFINGERPRINT", "idem-fail", now.Add(3*time.Minute))
	if apiErrorCode(err) != "FINGERPRINT_MISMATCH" {
		t.Fatalf("expected FINGERPRINT_MISMATCH, got %v", err)
	}

	// Confirm rotation with correct fingerprint
	newTrust, err := service.ConfirmSSHHostKeyRotation(proj.ID, obs2.ID, "user-1", fp2, "idem-confirm", now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("ConfirmSSHHostKeyRotation failed: %v", err)
	}
	if newTrust.Fingerprint != fp2 || newTrust.Status != TrustStatusActive {
		t.Fatalf("invalid new trust: %+v", newTrust)
	}

	// Old trust must now be superseded
	staleTrust, err := service.GetSSHHostKeyTrust(proj.ID, oldTrust.ID)
	if err != nil || staleTrust.Status != TrustStatusSuperseded {
		t.Fatalf("expected old trust to be superseded, got %+v", staleTrust)
	}

	// Active trust for host/port must now be newTrust
	active, err := service.GetActiveSSHHostKeyTrust(proj.ID, host, port)
	if err != nil || active.ID != newTrust.ID {
		t.Fatalf("expected active trust %s, got %s", newTrust.ID, active.ID)
	}
}

func TestSSHTrust_WorkerMismatch_And_ResumeSession(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	proj, _ := service.CreateProject("org-1", "Mismatch Test", "proj-mis", "user-1", "key-proj-mis")
	host := "203.0.113.70"
	port := 22

	pubKey1, fp1 := generateTestSSHKey(t)
	pubKey2, fp2 := generateTestSSHKey(t)

	// 1. Initial bootstrap
	obs1, _ := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKey1, fp1, "user-1", now)
	session, err := service.CreateBootstrapSession(proj.ID, "first_server", host, "root", "password", "user-1", "boot-1", port, obs1.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Lease session to worker-1
	lease, ok, err := service.LeaseNextBootstrapSession("worker-1", "", now, 90*time.Second)
	if err != nil || !ok {
		t.Fatalf("lease failed ok=%v err=%v", ok, err)
	}
	if lease.Session.AttemptCount != 1 {
		t.Fatalf("expected attempt_count 1, got %d", lease.Session.AttemptCount)
	}

	// 3. Worker encounters host key mismatch and reports Finish with waiting_host_key_confirmation
	finished, err := service.FinishBootstrapSessionForLease(proj.ID, session.ID, "worker-1", lease.LeaseToken, BootstrapFinishResult{
		Status:              "waiting_host_key_confirmation",
		FailureCode:         "SSH_HOST_KEY_MISMATCH",
		MessageRedacted:     "host key differs from pinned identity",
		ObservedAlgorithm:   ssh.KeyAlgoED25519,
		ObservedPublicKey:   pubKey2,
		ObservedFingerprint: fp2,
	}, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("FinishBootstrapSessionForLease failed: %v", err)
	}

	// Verify session state:
	// - status is waiting_host_key_confirmation
	// - attempt_count is NOT incremented (remains 1)
	// - next_attempt_at is nil (no auto-retry)
	// - dead_lettered_at is nil
	// - lease is cleared
	if finished.Status != BootstrapWaitingHostKeyConfirmation {
		t.Fatalf("expected status waiting_host_key_confirmation, got %s", finished.Status)
	}
	if finished.AttemptCount != 1 {
		t.Fatalf("expected attempt_count 1, got %d", finished.AttemptCount)
	}
	if finished.NextAttemptAt != nil {
		t.Fatalf("expected next_attempt_at nil, got %v", finished.NextAttemptAt)
	}
	if finished.DeadLetteredAt != nil {
		t.Fatalf("expected dead_lettered_at nil, got %v", finished.DeadLetteredAt)
	}
	if finished.LeaseOwner != "" {
		t.Fatalf("expected lease_owner empty, got %s", finished.LeaseOwner)
	}

	// Verify an observation was created for the mismatch
	var foundObs *SSHHostKeyObservation
	for _, o := range service.sshObservations {
		if o.Fingerprint == fp2 && o.TrustState == TrustStateChanged {
			foundObs = &o
			break
		}
	}
	if foundObs == nil {
		t.Fatal("expected changed observation to be created for observed key 2")
	}
	if foundObs.PreviousFingerprint != fp1 {
		t.Fatalf("expected previous fingerprint %s, got %s", fp1, foundObs.PreviousFingerprint)
	}

	// 4. Operator confirms rotation for foundObs
	_, err = service.ConfirmSSHHostKeyRotation(proj.ID, foundObs.ID, "user-1", fp2, "idem-confirm", now.Add(20*time.Second))
	if err != nil {
		t.Fatalf("ConfirmSSHHostKeyRotation failed: %v", err)
	}

	// 5. Operator resumes the session with confirmed probe ID
	resumed, err := service.ResumeBootstrapSession(proj.ID, session.ID, foundObs.ID, "user-1", "idem-resume", now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("ResumeBootstrapSession failed: %v", err)
	}

	// Session is now pending again, with updated trust ID and exact same checkpoint
	if resumed.Status != BootstrapPending {
		t.Fatalf("expected status pending after resume, got %s", resumed.Status)
	}
	newTrust, _ := service.GetActiveSSHHostKeyTrust(proj.ID, host, port)
	if resumed.SSHHostKeyTrustID != newTrust.ID {
		t.Fatalf("expected trust ID %s, got %s", newTrust.ID, resumed.SSHHostKeyTrustID)
	}
	if resumed.Checkpoint != session.Checkpoint {
		t.Fatalf("expected checkpoint preserved, got %+v", resumed.Checkpoint)
	}

	// 6. Worker-2 can now lease the resumed session
	secondLease, ok, err := service.LeaseNextBootstrapSession("worker-2", "", now.Add(35*time.Second), 90*time.Second)
	if err != nil || !ok {
		t.Fatalf("second lease failed: ok=%v err=%v", ok, err)
	}
	if secondLease.Session.ID != session.ID {
		t.Fatalf("expected to lease resumed session %s, got %s", session.ID, secondLease.Session.ID)
	}
	if secondLease.Session.AttemptCount != 2 {
		t.Fatalf("expected attempt_count 2 on second lease, got %d", secondLease.Session.AttemptCount)
	}
}

func TestSSHTrust_ExpiredProbeRejection(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	proj, _ := service.CreateProject("org-1", "Expiry Test", "proj-exp", "user-1", "key-proj-exp")
	pubKey, fp := generateTestSSHKey(t)
	host := "203.0.113.80"
	port := 22

	obs, _ := service.CreateSSHHostKeyObservation(proj.ID, host, port, host, ssh.KeyAlgoED25519, pubKey, fp, "user-1", now)

	// Advance time past 10 minutes expiry
	service.now = func() time.Time { return now.Add(11 * time.Minute) }

	// Bootstrap create must reject expired probe
	_, err := service.CreateBootstrapSession(proj.ID, "first_server", host, "root", "password", "user-1", "boot-exp", port, obs.ID)
	if apiErrorCode(err) != "SSH_HOST_KEY_PROBE_EXPIRED" {
		t.Fatalf("expected SSH_HOST_KEY_PROBE_EXPIRED, got %v", err)
	}

	// Confirm rotation must reject expired probe
	_, err = service.ConfirmSSHHostKeyRotation(proj.ID, obs.ID, "user-1", fp, "idem-exp", now.Add(11*time.Minute))
	if apiErrorCode(err) != "SSH_HOST_KEY_PROBE_EXPIRED" {
		t.Fatalf("expected SSH_HOST_KEY_PROBE_EXPIRED on confirm, got %v", err)
	}
}
