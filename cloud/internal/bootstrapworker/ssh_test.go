package bootstrapworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
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

func TestSSHConnectRequiresKnownHostsBeforeDial(t *testing.T) {
	_, err := (SSHExecutor{}).Connect(context.Background(), RemoteTarget{Host: "127.0.0.1", Port: 1, Username: "root", Password: "secret"})
	if !errors.Is(err, ErrSSHHostKeyVerificationRequired) {
		t.Fatalf("error=%v", err)
	}
}

func TestSSHConnectMatchingHostKeySucceeds(t *testing.T) {
	signer := newSSHSigner(t)
	host, port := startSSHServer(t, signer)
	knownHosts := writeKnownHosts(t, net.JoinHostPort(host, strconv.Itoa(port)), signer.PublicKey(), 0o600)
	session, err := (SSHExecutor{KnownHostsPath: knownHosts}).Connect(context.Background(), RemoteTarget{Host: host, Port: port, Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConnectUnknownHostFailsClosed(t *testing.T) {
	signer := newSSHSigner(t)
	host, port := startSSHServer(t, signer)
	knownHosts := writeKnownHosts(t, "other.example:22", signer.PublicKey(), 0o600)
	_, err := (SSHExecutor{KnownHostsPath: knownHosts}).Connect(context.Background(), RemoteTarget{Host: host, Port: port, Username: "root", Password: "secret"})
	if !errors.Is(err, ErrSSHHostKeyVerificationFailed) {
		t.Fatalf("error=%v", err)
	}
}

func TestSSHConnectKeyMismatchFailsClosed(t *testing.T) {
	serverSigner := newSSHSigner(t)
	host, port := startSSHServer(t, serverSigner)
	knownHosts := writeKnownHosts(t, net.JoinHostPort(host, strconv.Itoa(port)), newSSHSigner(t).PublicKey(), 0o600)
	_, err := (SSHExecutor{KnownHostsPath: knownHosts}).Connect(context.Background(), RemoteTarget{Host: host, Port: port, Username: "root", Password: "secret"})
	if !errors.Is(err, ErrSSHHostKeyVerificationFailed) {
		t.Fatalf("error=%v", err)
	}
}

func TestSSHConnectNonstandardPortKnownHostsFormat(t *testing.T) {
	signer := newSSHSigner(t)
	host, port := startSSHServer(t, signer)
	if port == 22 {
		t.Fatal("test server unexpectedly used port 22")
	}
	entry := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	knownHosts := writeKnownHosts(t, entry, signer.PublicKey(), 0o640)
	session, err := (SSHExecutor{KnownHostsPath: knownHosts}).Connect(context.Background(), RemoteTarget{Host: host, Port: port, Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
}

func TestSSHKnownHostsRejectsWritableFileAndSymlink(t *testing.T) {
	for name, makePath := range map[string]func(*testing.T) string{
		"group writable": func(t *testing.T) string {
			return writeKnownHosts(t, "example.test", newSSHSigner(t).PublicKey(), 0o620)
		},
		"world writable": func(t *testing.T) string {
			return writeKnownHosts(t, "example.test", newSSHSigner(t).PublicKey(), 0o606)
		},
		"symlink": func(t *testing.T) string {
			target := writeKnownHosts(t, "example.test", newSSHSigner(t).PublicKey(), 0o600)
			link := filepath.Join(t.TempDir(), "known_hosts")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (SSHExecutor{KnownHostsPath: makePath(t)}).Connect(context.Background(), RemoteTarget{Host: "127.0.0.1", Port: 1, Username: "root", Password: "secret"})
			if !errors.Is(err, ErrSSHHostKeyVerificationFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSSHVerificationErrorsDoNotExposeCredentials(t *testing.T) {
	privateKey := "private-key-must-not-appear"
	password := "password-must-not-appear"
	_, err := (SSHExecutor{}).Connect(context.Background(), RemoteTarget{Host: "127.0.0.1", Port: 1, Username: "root", Password: password, PrivateKey: privateKey})
	if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), privateKey) {
		t.Fatalf("credential leaked in error: %v", err)
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

func startSSHServer(t *testing.T, signer ssh.Signer) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serverConn, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		defer serverConn.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			_ = channel.Reject(ssh.UnknownChannelType, "test server does not accept channels")
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

func writeKnownHosts(t *testing.T, host string, key ssh.PublicKey, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
