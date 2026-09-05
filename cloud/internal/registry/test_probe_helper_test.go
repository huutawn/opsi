package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func seedTestProbe(t testing.TB, reg API, projectID, host string, port int) string {
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
	obs, err := reg.CreateSSHHostKeyObservation(projectID, host, port, host, ssh.KeyAlgoED25519, pubKey, fingerprint, "test-user", time.Now().UTC())
	if err != nil {
		t.Fatalf("seedTestProbe failed: %v", err)
	}
	return obs.ID
}
