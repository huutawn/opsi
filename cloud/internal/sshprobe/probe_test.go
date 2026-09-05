package sshprobe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestIsPublicRoutableIP(t *testing.T) {
	cases := []struct {
		ip    string
		valid bool
	}{
		// Blocked loopback
		{"127.0.0.1", false},
		{"127.0.1.1", false},
		{"::1", false},

		// Blocked private IPv4 (RFC 1918)
		{"10.0.0.1", false},
		{"10.254.0.1", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"192.168.0.254", false},

		// Blocked CGNAT (RFC 6598)
		{"100.64.0.1", false},
		{"100.127.255.255", false},

		// Blocked Link-Local
		{"169.254.1.1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},

		// Blocked Multicast and Unspecified
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
		{"239.255.255.250", false},
		{"ff02::1", false},

		// Blocked Broadcast
		{"255.255.255.255", false},

		// Blocked IPv6 Unique Local (ULA)
		{"fc00::1", false},
		{"fd12:3456:789a:1::1", false},

		// Allowed Public IPs
		{"1.1.1.1", true},
		{"8.8.8.8", true},
		{"103.252.137.163", true},
		{"203.0.113.10", true},
		{"2606:4700:4700::1111", true},
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("failed to parse test ip %s", tc.ip)
		}
		got := IsPublicRoutableIP(ip)
		if got != tc.valid {
			t.Errorf("IsPublicRoutableIP(%s) = %v; want %v", tc.ip, got, tc.valid)
		}
	}
}

func TestResolveAndValidate_BlocksPrivate(t *testing.T) {
	svc := &Service{AllowPrivateIPs: false}
	ctx := context.Background()

	// Direct IP checks
	for _, privateIP := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254"} {
		_, err := svc.ResolveAndValidate(ctx, privateIP)
		if !errors.Is(err, ErrNonPublicAddress) {
			t.Errorf("ResolveAndValidate(%s) expected ErrNonPublicAddress, got %v", privateIP, err)
		}
	}

	// Host resolving to private IP
	svcWithResolver := &Service{
		AllowPrivateIPs: false,
		Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.168.1.50")}, nil
		},
	}
	_, err := svcWithResolver.ResolveAndValidate(ctx, "internal.example.com")
	if !errors.Is(err, ErrNonPublicAddress) {
		t.Errorf("expected ErrNonPublicAddress for resolved private IP, got %v", err)
	}

	// Host resolving to mixed (one public, one private) should fail closed
	svcWithMixedResolver := &Service{
		AllowPrivateIPs: false,
		Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("127.0.0.1")}, nil
		},
	}
	_, err = svcWithMixedResolver.ResolveAndValidate(ctx, "mixed.example.com")
	if !errors.Is(err, ErrNonPublicAddress) {
		t.Errorf("expected ErrNonPublicAddress for mixed IP list, got %v", err)
	}
}

func startTestSSHServer(t *testing.T, hostKey ssh.Signer) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}

	config := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	config.AddHostKey(hostKey)

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

	tcpAddr := listener.Addr().(*net.TCPAddr)
	return listener, tcpAddr.Port
}

func TestProbe_HandshakeWithoutAuth(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	listener, port := startTestSSHServer(t, signer)
	defer listener.Close()

	svc := &Service{
		Timeout:         2 * time.Second,
		AllowPrivateIPs: true, // For localhost test server
	}

	res, err := svc.Probe(context.Background(), ProbeTarget{
		Host: "127.0.0.1",
		Port: port,
	})
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	expectedFingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if res.Fingerprint != expectedFingerprint {
		t.Errorf("got fingerprint %q, want %q", res.Fingerprint, expectedFingerprint)
	}
	if res.Algorithm != ssh.KeyAlgoED25519 {
		t.Errorf("got algorithm %q, want %q", res.Algorithm, ssh.KeyAlgoED25519)
	}
	if res.ResolvedIP != "127.0.0.1" {
		t.Errorf("got resolved_ip %q, want 127.0.0.1", res.ResolvedIP)
	}
}

func TestProbe_DNSRebindingDetected(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	listener, port := startTestSSHServer(t, signer)
	defer listener.Close()

	svc := &Service{
		Timeout:         2 * time.Second,
		AllowPrivateIPs: true,
		Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
			// Resolver claims host is 127.0.0.2
			return []net.IP{net.ParseIP("127.0.0.2")}, nil
		},
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			// But dialer actually connects to 127.0.0.1 (simulating DNS rebinding)
			return net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		},
	}

	_, err = svc.Probe(context.Background(), ProbeTarget{
		Host: "rebind.example.com",
		Port: port,
	})
	if !errors.Is(err, ErrDNSRebindingDetected) {
		t.Fatalf("expected ErrDNSRebindingDetected, got %v", err)
	}
}

func TestProbe_Timeout(t *testing.T) {
	// Listener that accepts but does not complete handshake
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Hold connection open without sending anything until client closes
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				_, _ = c.Read(buf)
			}(conn)
		}
	}()

	svc := &Service{
		Timeout:         100 * time.Millisecond,
		AllowPrivateIPs: true,
	}

	_, err = svc.Probe(context.Background(), ProbeTarget{
		Host: "127.0.0.1",
		Port: port,
	})
	if err == nil {
		t.Fatal("expected probe timeout error, got nil")
	}
}
