package bootstrapworker

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

func TestSSHConnectRequiresPinnedHostKey(t *testing.T) {
	_, err := (SSHExecutor{}).Connect(context.Background(), RemoteTarget{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "root",
		Password: "secret",
		HostKey:  HostKeyConfig{},
	})
	if !errors.Is(err, ErrSSHHostKeyVerificationRequired) {
		t.Fatalf("expected ErrSSHHostKeyVerificationRequired, got %v", err)
	}
}

func TestSSHConnectMatchingHostKeySucceeds(t *testing.T) {
	signer := newSSHSigner(t)
	host, port := startSSHServer(t, signer)

	pubKeyBase64 := base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	fp := ssh.FingerprintSHA256(signer.PublicKey())

	target := RemoteTarget{
		Host:     host,
		Port:     port,
		Username: "root",
		Password: "secret",
		HostKey: HostKeyConfig{
			Algorithm:   signer.PublicKey().Type(),
			PublicKey:   pubKeyBase64,
			Fingerprint: fp,
		},
	}

	session, err := (SSHExecutor{}).Connect(context.Background(), target)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConnectUsesPinnedAlgorithmWhenServerOffersMultipleKeys(t *testing.T) {
	ed25519Signer := newSSHSigner(t)
	ecdsaSigner := newECDSASigner(t)
	host, port := startSSHServerWithSigners(t, ecdsaSigner, ed25519Signer)

	pubKeyBase64 := base64.StdEncoding.EncodeToString(ed25519Signer.PublicKey().Marshal())
	fp := ssh.FingerprintSHA256(ed25519Signer.PublicKey())

	target := RemoteTarget{
		Host:     host,
		Port:     port,
		Username: "root",
		Password: "secret",
		HostKey: HostKeyConfig{
			Algorithm:   ssh.KeyAlgoED25519,
			PublicKey:   pubKeyBase64,
			Fingerprint: fp,
		},
	}

	session, err := (SSHExecutor{}).Connect(context.Background(), target)
	if err != nil {
		t.Fatalf("Connect with pinned algorithm failed: %v", err)
	}
	_ = session.Close()
}

func TestSSHConnectPinnedRSAHostKeyAllowsRSASHA2(t *testing.T) {
	rsaSigner := newRSASigner(t)
	modernRSASigner, err := ssh.NewSignerWithAlgorithms(rsaSigner.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256})
	if err != nil {
		t.Fatal(err)
	}
	host, port := startSSHServer(t, modernRSASigner)

	pubKeyBase64 := base64.StdEncoding.EncodeToString(rsaSigner.PublicKey().Marshal())
	fp := ssh.FingerprintSHA256(rsaSigner.PublicKey())

	target := RemoteTarget{
		Host:     host,
		Port:     port,
		Username: "root",
		Password: "secret",
		HostKey: HostKeyConfig{
			Algorithm:   ssh.KeyAlgoRSA,
			PublicKey:   pubKeyBase64,
			Fingerprint: fp,
		},
	}

	session, err := (SSHExecutor{}).Connect(context.Background(), target)
	if err != nil {
		t.Fatalf("Connect with pinned RSA key failed: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConnectKeyMismatchReturnsStructuredObservation(t *testing.T) {
	serverSigner := newSSHSigner(t)
	host, port := startSSHServer(t, serverSigner)

	otherSigner := newSSHSigner(t)
	otherPubKeyBase64 := base64.StdEncoding.EncodeToString(otherSigner.PublicKey().Marshal())
	otherFP := ssh.FingerprintSHA256(otherSigner.PublicKey())

	target := RemoteTarget{
		Host:     host,
		Port:     port,
		Username: "root",
		Password: "secret",
		HostKey: HostKeyConfig{
			Algorithm:   ssh.KeyAlgoED25519,
			PublicKey:   otherPubKeyBase64,
			Fingerprint: otherFP,
		},
	}

	_, err := (SSHExecutor{}).Connect(context.Background(), target)
	if err == nil {
		t.Fatal("expected error on host key mismatch, got nil")
	}

	var mismatch ErrHostKeyMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected ErrHostKeyMismatch, got %T (%v)", err, err)
	}

	expectedObservedFP := ssh.FingerprintSHA256(serverSigner.PublicKey())
	if mismatch.Fingerprint != expectedObservedFP {
		t.Fatalf("expected observed fingerprint %s, got %s", expectedObservedFP, mismatch.Fingerprint)
	}
	if mismatch.Algorithm != ssh.KeyAlgoED25519 {
		t.Fatalf("expected observed algorithm %s, got %s", ssh.KeyAlgoED25519, mismatch.Algorithm)
	}
}

func TestSSHConnectDialUsesResolvedIP(t *testing.T) {
	signer := newSSHSigner(t)
	host, port := startSSHServer(t, signer)

	pubKeyBase64 := base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	fp := ssh.FingerprintSHA256(signer.PublicKey())

	target := RemoteTarget{
		Host:       "logical-node-name.example",
		ResolvedIP: host, // 127.0.0.1
		Port:       port,
		Username:   "root",
		Password:   "secret",
		HostKey: HostKeyConfig{
			Algorithm:   signer.PublicKey().Type(),
			PublicKey:   pubKeyBase64,
			Fingerprint: fp,
		},
	}

	session, err := (SSHExecutor{}).Connect(context.Background(), target)
	if err != nil {
		t.Fatalf("Connect with ResolvedIP failed: %v", err)
	}
	_ = session.Close()
}

func TestSSHVerificationErrorsDoNotExposeCredentials(t *testing.T) {
	privateKey := "private-key-must-not-appear"
	password := "password-must-not-appear"
	_, err := (SSHExecutor{}).Connect(context.Background(), RemoteTarget{
		Host:       "127.0.0.1",
		Port:       1,
		Username:   "root",
		Password:   password,
		PrivateKey: privateKey,
		HostKey: HostKeyConfig{
			Algorithm:   "ssh-ed25519",
			PublicKey:   "AAAA",
			Fingerprint: "SHA256:dummy",
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, secret := range []string{privateKey, password} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked in error: %v", err)
		}
	}
}

func TestSSHSourceHasNoInsecureHostKeyFallback(t *testing.T) {
	data, err := os.ReadFile("ssh.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Insecure"+"IgnoreHostKey") {
		t.Fatal("insecure SSH host-key fallback remains")
	}
}

func newSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func newECDSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func newRSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func startSSHServer(t *testing.T, signer ssh.Signer) (string, int) {
	return startSSHServerWithSigners(t, signer)
}

func startSSHServerWithSigners(t *testing.T, signers ...ssh.Signer) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		return nil, nil
	}}
	for _, signer := range signers {
		serverConfig.AddHostKey(signer)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				serverConn, channels, requests, err := ssh.NewServerConn(c, serverConfig)
				if err != nil {
					return
				}
				defer serverConn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.UnknownChannelType, "test server does not accept channels")
				}
			}(conn)
		}
	}()
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
