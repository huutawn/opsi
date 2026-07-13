package bootstrapworker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestSSHAuthMethodsSupportsPasswordAndPrivateKey(t *testing.T) {
	if methods, err := sshAuthMethods(RemoteTarget{Password: "secret"}); err != nil || len(methods) != 1 {
		t.Fatalf("password methods=%d err=%v", len(methods), err)
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if methods, err := sshAuthMethods(RemoteTarget{PrivateKey: string(key)}); err != nil || len(methods) != 1 {
		t.Fatalf("private-key methods=%d err=%v", len(methods), err)
	}

	if _, err := sshAuthMethods(RemoteTarget{Password: "secret", PrivateKey: string(key)}); err == nil {
		t.Fatal("mixed SSH credentials were accepted")
	}
	if _, err := sshAuthMethods(RemoteTarget{PrivateKey: "not-a-key"}); err == nil {
		t.Fatal("invalid SSH private key was accepted")
	}
}
