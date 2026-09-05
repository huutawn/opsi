package sshprobe

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrInvalidHost          = errors.New("invalid or empty host")
	ErrInvalidPort          = errors.New("invalid SSH port; must be between 1 and 65535")
	ErrNonPublicAddress     = errors.New("target address is not a publicly routable IP")
	ErrDNSRebindingDetected = errors.New("dialed IP does not match verified resolved IP (possible DNS rebinding)")
	ErrUnsupportedAlgorithm = errors.New("unsupported host key algorithm; DSA and legacy algorithms are rejected")
	ErrProbeFailed          = errors.New("ssh probe failed")
	errHandshakeComplete    = errors.New("ssh probe handshake completed")
)

// SupportedHostKeyAlgorithms defines allowed modern algorithms: ED25519, ECDSA, and RSA SHA-2.
// DSA (ssh-dss) and legacy ciphers are strictly excluded.
var SupportedHostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256,
	ssh.KeyAlgoECDSA384,
	ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512,
	ssh.KeyAlgoRSASHA256,
	ssh.KeyAlgoRSA,
}

type ProbeTarget struct {
	Host string
	Port int
}

type ProbeResult struct {
	Host        string
	Port        int
	ResolvedIP  string
	Algorithm   string
	PublicKey   string
	Fingerprint string
}

type Service struct {
	Timeout         time.Duration
	AllowPrivateIPs bool // Only enabled in testing environments
	Dialer          func(ctx context.Context, network, address string) (net.Conn, error)
	Resolver        func(ctx context.Context, host string) ([]net.IP, error)
}

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 15 * time.Second
}

// IsPublicRoutableIP checks if an IP address is a publicly routable global unicast address.
// Blocks loopback, RFC 1918 private, link-local, multicast, unspecified, and non-routable ranges.
func IsPublicRoutableIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Check standard properties
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}

	// Additional IPv4 checks
	if v4 := ip.To4(); v4 != nil {
		// 0.0.0.0/8 (Current network)
		if v4[0] == 0 {
			return false
		}
		// 100.64.0.0/10 (Shared Address Space / CGNAT RFC 6598)
		if v4[0] == 100 && (v4[1]&0xc0) == 64 {
			return false
		}
		// 127.0.0.0/8 (Loopback)
		if v4[0] == 127 {
			return false
		}
		// 169.254.0.0/16 (Link-Local)
		if v4[0] == 169 && v4[1] == 254 {
			return false
		}
		// 224.0.0.0/4 and 240.0.0.0/4 (Multicast / Reserved)
		if v4[0] >= 224 {
			return false
		}
		// 255.255.255.255 (Broadcast)
		if v4[0] == 255 && v4[1] == 255 && v4[2] == 255 && v4[3] == 255 {
			return false
		}
		return true
	}

	// Additional IPv6 checks
	// Unique Local Address (fc00::/7)
	if (ip[0] & 0xfe) == 0xfc {
		return false
	}
	return true
}

// ResolveAndValidate resolves the target host and checks that every resolved IP is publicly routable.
func (s *Service) ResolveAndValidate(ctx context.Context, host string) (net.IP, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, ErrInvalidHost
	}

	// If host is already an IP address
	if parsed := net.ParseIP(trimmed); parsed != nil {
		if !s.AllowPrivateIPs && !IsPublicRoutableIP(parsed) {
			return nil, fmt.Errorf("%w: %s", ErrNonPublicAddress, parsed.String())
		}
		return parsed, nil
	}

	// Resolve via resolver
	var ips []net.IP
	if s.Resolver != nil {
		resolved, err := s.Resolver(ctx, trimmed)
		if err != nil {
			return nil, fmt.Errorf("resolve host %q: %w", trimmed, err)
		}
		ips = resolved
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", trimmed)
		if err != nil {
			return nil, fmt.Errorf("resolve host %q: %w", trimmed, err)
		}
		ips = resolved
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve host %q: no IP addresses found", trimmed)
	}

	// Validate ALL resolved addresses against public routable rules to prevent rebinding
	var chosen net.IP
	for _, ip := range ips {
		if !s.AllowPrivateIPs && !IsPublicRoutableIP(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrNonPublicAddress, trimmed, ip.String())
		}
		if chosen == nil {
			chosen = ip
		}
	}

	return chosen, nil
}

// Probe connects to target over TCP, checks against DNS rebinding, performs SSH handshake
// without authentication, and extracts the host public key and fingerprint.
func (s *Service) Probe(ctx context.Context, target ProbeTarget) (ProbeResult, error) {
	trimmedHost := strings.TrimSpace(target.Host)
	if trimmedHost == "" {
		return ProbeResult{}, ErrInvalidHost
	}
	if target.Port <= 0 || target.Port > 65535 {
		return ProbeResult{}, ErrInvalidPort
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	// 1. Resolve and validate IP address
	resolvedIP, err := s.ResolveAndValidate(timeoutCtx, trimmedHost)
	if err != nil {
		return ProbeResult{}, err
	}

	// 2. Dial TCP directly to the verified resolved IP
	destAddr := net.JoinHostPort(resolvedIP.String(), strconv.Itoa(target.Port))
	var conn net.Conn
	if s.Dialer != nil {
		conn, err = s.Dialer(timeoutCtx, "tcp", destAddr)
	} else {
		dialer := net.Dialer{Timeout: s.timeout()}
		conn, err = dialer.DialContext(timeoutCtx, "tcp", destAddr)
	}
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: dial error: %v", ErrProbeFailed, err)
	}
	defer conn.Close()

	// 3. Verify dialed remote address matches verified resolved IP (anti-rebinding check)
	remoteAddr := conn.RemoteAddr()
	tcpAddr, ok := remoteAddr.(*net.TCPAddr)
	if !ok {
		return ProbeResult{}, fmt.Errorf("%w: non-TCP remote address: %v", ErrProbeFailed, remoteAddr)
	}
	if !s.AllowPrivateIPs && !IsPublicRoutableIP(tcpAddr.IP) {
		return ProbeResult{}, fmt.Errorf("%w: dialed remote IP is not public: %s", ErrNonPublicAddress, tcpAddr.IP.String())
	}
	if !tcpAddr.IP.Equal(resolvedIP) {
		return ProbeResult{}, fmt.Errorf("%w: dialed %s, expected %s", ErrDNSRebindingDetected, tcpAddr.IP.String(), resolvedIP.String())
	}

	// 4. SSH handshake stopping BEFORE authentication
	var observedKey ssh.PublicKey
	var observedAlgo string

	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if key.Type() == "ssh-dss" {
			return ErrUnsupportedAlgorithm
		}
		observedKey = key
		observedAlgo = key.Type()
		// Abort immediately with sentinel error so no auth packets are sent
		return errHandshakeComplete
	}

	sshConfig := &ssh.ClientConfig{
		User:              "probe", // Dummy user, no auth method supplied
		Auth:              nil,     // No credentials
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: SupportedHostKeyAlgorithms,
		Timeout:           s.timeout(),
	}

	_ = conn.SetDeadline(time.Now().Add(s.timeout()))

	sshConn, _, _, handshakeErr := ssh.NewClientConn(conn, net.JoinHostPort(trimmedHost, strconv.Itoa(target.Port)), sshConfig)
	if sshConn != nil {
		_ = sshConn.Close()
	}

	if observedKey == nil {
		if handshakeErr != nil {
			if errors.Is(handshakeErr, ErrUnsupportedAlgorithm) {
				return ProbeResult{}, ErrUnsupportedAlgorithm
			}
			return ProbeResult{}, fmt.Errorf("%w: handshake error: %v", ErrProbeFailed, handshakeErr)
		}
		return ProbeResult{}, fmt.Errorf("%w: no host key received during handshake", ErrProbeFailed)
	}

	pubKeyBase64 := base64.StdEncoding.EncodeToString(observedKey.Marshal())
	fingerprint := ssh.FingerprintSHA256(observedKey)

	return ProbeResult{
		Host:        trimmedHost,
		Port:        target.Port,
		ResolvedIP:  tcpAddr.IP.String(),
		Algorithm:   observedAlgo,
		PublicKey:   pubKeyBase64,
		Fingerprint: fingerprint,
	}, nil
}

// ComparePublicKeys securely compares two base64-encoded public keys in constant time.
func ComparePublicKeys(a, b string) bool {
	rawA, errA := base64.StdEncoding.DecodeString(strings.TrimSpace(a))
	rawB, errB := base64.StdEncoding.DecodeString(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	return subtle.ConstantTimeCompare(rawA, rawB) == 1
}
