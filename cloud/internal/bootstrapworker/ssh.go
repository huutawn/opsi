package bootstrapworker

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrSSHHostKeyVerificationRequired = errors.New("SSH host-key verification requires pinned host key")
	ErrSSHHostKeyVerificationFailed   = errors.New("SSH host-key verification failed")
)

type ErrHostKeyMismatch struct {
	Algorithm   string
	PublicKey   string
	Fingerprint string
}

func (e ErrHostKeyMismatch) Error() string {
	return fmt.Sprintf("SSH host-key verification failed: host key differs from pinned identity (observed: %s)", e.Fingerprint)
}

type HostKeyConfig struct {
	Algorithm   string `json:"algorithm"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

type RemoteTarget struct {
	Host       string
	ResolvedIP string
	Port       int
	Username   string
	Password   string
	PrivateKey string
	HostKey    HostKeyConfig
}

type CommandSpec struct {
	Script       string
	Env          map[string]string
	SensitiveEnv map[string]string
}

type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type RemoteExecutor interface {
	Connect(context.Context, RemoteTarget) (RemoteSession, error)
}

type RemoteSession interface {
	Run(context.Context, CommandSpec) (CommandResult, error)
	Close() error
}

type SSHExecutor struct{}

func (e SSHExecutor) Connect(ctx context.Context, target RemoteTarget) (RemoteSession, error) {
	if target.Port == 0 {
		target.Port = 22
	}
	if strings.TrimSpace(target.HostKey.PublicKey) == "" {
		return nil, ErrSSHHostKeyVerificationRequired
	}

	expectedKeyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(target.HostKey.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pinned public key: %v", ErrSSHHostKeyVerificationFailed, err)
	}

	hostKeyAlgorithms := pinnedHostKeyAlgorithms(target.HostKey.Algorithm)

	var mismatchErr *ErrHostKeyMismatch
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		keyBytes := key.Marshal()
		if subtle.ConstantTimeCompare(keyBytes, expectedKeyBytes) == 1 {
			return nil
		}
		observedAlgo := key.Type()
		observedPubKey := base64.StdEncoding.EncodeToString(keyBytes)
		observedFingerprint := ssh.FingerprintSHA256(key)
		mismatchErr = &ErrHostKeyMismatch{
			Algorithm:   observedAlgo,
			PublicKey:   observedPubKey,
			Fingerprint: observedFingerprint,
		}
		return *mismatchErr
	}

	authMethods, err := sshAuthMethods(target)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:              target.Username,
		Auth:              authMethods,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           15 * time.Second,
	}

	dialHost := target.Host
	if target.ResolvedIP != "" {
		dialHost = target.ResolvedIP
	}

	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(dialHost, strconv.Itoa(target.Port)))
	if err != nil {
		return nil, err
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)), cfg)
	if err != nil {
		_ = conn.Close()
		if mismatchErr != nil {
			return nil, *mismatchErr
		}
		var mismatch ErrHostKeyMismatch
		if errors.As(err, &mismatch) {
			return nil, mismatch
		}
		return nil, err
	}

	return sshSession{client: ssh.NewClient(c, chans, reqs)}, nil
}

func pinnedHostKeyAlgorithms(algo string) []string {
	if algo == ssh.KeyAlgoRSA || strings.HasPrefix(algo, "rsa") {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	if algo != "" {
		return []string{algo}
	}
	return []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA512,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSA,
	}
}

func sshAuthMethods(target RemoteTarget) ([]ssh.AuthMethod, error) {
	switch {
	case target.PrivateKey != "" && target.Password != "":
		return nil, errors.New("ssh target must use exactly one authentication method")
	case target.PrivateKey != "":
		signer, err := ssh.ParsePrivateKey([]byte(target.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("parse ssh private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case target.Password != "":
		return []ssh.AuthMethod{ssh.Password(target.Password)}, nil
	default:
		return nil, errors.New("ssh credential is required")
	}
}

type sshSession struct {
	client *ssh.Client
}

func (s sshSession) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return CommandResult{ExitCode: 255}, err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	stdin, err := session.StdinPipe()
	if err != nil {
		return CommandResult{ExitCode: 255}, err
	}
	if err := session.Start("sh -s"); err != nil {
		return CommandResult{ExitCode: 255}, err
	}
	go func() {
		defer stdin.Close()
		_, _ = stdin.Write([]byte(renderScript(spec)))
	}()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return CommandResult{ExitCode: 255, Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case err := <-done:
		code := 0
		if err != nil {
			code = 1
			var exitErr *ssh.ExitError
			if ok := asExitError(err, &exitErr); ok {
				code = exitErr.ExitStatus()
			}
		}
		return CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
}

func (s sshSession) Close() error { return s.client.Close() }

func renderScript(spec CommandSpec) string {
	var b bytes.Buffer
	b.WriteString("set -eu\n")
	keys := make([]string, 0, len(spec.Env)+len(spec.SensitiveEnv))
	env := map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
		keys = append(keys, k)
	}
	for k, v := range spec.SensitiveEnv {
		env[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(env[k]))
		b.WriteString("\n")
	}
	b.WriteString(spec.Script)
	b.WriteString("\n")
	return b.String()
}

func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}

func asExitError(err error, target **ssh.ExitError) bool {
	return errors.As(err, target)
}
