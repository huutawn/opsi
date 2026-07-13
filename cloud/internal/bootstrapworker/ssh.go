package bootstrapworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	ErrSSHHostKeyVerificationRequired = errors.New("SSH host-key verification requires a known_hosts file")
	ErrSSHHostKeyVerificationFailed   = errors.New("SSH host-key verification failed")
)

type RemoteTarget struct {
	Host       string
	Port       int
	Username   string
	Password   string
	PrivateKey string
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

type SSHExecutor struct {
	KnownHostsPath string
}

func (e SSHExecutor) Connect(ctx context.Context, target RemoteTarget) (RemoteSession, error) {
	if target.Port == 0 {
		target.Port = 22
	}
	if e.KnownHostsPath == "" {
		return nil, ErrSSHHostKeyVerificationRequired
	}
	if err := validateKnownHostsFile(e.KnownHostsPath, false); err != nil {
		return nil, fmt.Errorf("%w: known_hosts file is invalid", ErrSSHHostKeyVerificationFailed)
	}
	knownHostsCallback, err := knownhosts.New(e.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("%w: known_hosts file could not be loaded", ErrSSHHostKeyVerificationFailed)
	}
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := knownHostsCallback(hostname, remote, key); err != nil {
			return ErrSSHHostKeyVerificationFailed
		}
		return nil
	}
	authMethods, err := sshAuthMethods(target)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            target.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return sshSession{client: ssh.NewClient(c, chans, reqs)}, nil
}

func validateKnownHostsFile(path string, requireNonEmpty bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("known_hosts path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("known_hosts path must be a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("known_hosts file must not be group/world writable")
	}
	if requireNonEmpty && info.Size() == 0 {
		return errors.New("known_hosts file must not be empty")
	}
	return nil
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
