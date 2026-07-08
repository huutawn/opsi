package bootstrapworker

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type RemoteTarget struct {
	Host     string
	Port     int
	Username string
	Password string
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
	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	if e.KnownHostsPath != "" {
		cb, err := knownhosts.New(e.KnownHostsPath)
		if err != nil {
			return nil, err
		}
		hostKeyCallback = cb
	}
	cfg := &ssh.ClientConfig{
		User:            target.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(target.Password)},
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
