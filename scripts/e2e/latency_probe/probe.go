package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

// RequestMetric captures detailed timings and metadata for a single HTTP probe call.
// Sensitive data (passwords, tokens, bodies) MUST NEVER be stored here.
type RequestMetric struct {
	Timestamp   time.Time `json:"timestamp"`
	Scenario    string    `json:"scenario"`
	VULevel     int       `json:"vu_level"`
	VUID        int       `json:"vu_id"`
	Sequence    uint64    `json:"sequence"`
	Step        string    `json:"step"` // "single", "redirect_1", "redirect_2", "composite"
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	StatusCode  int       `json:"status_code"`
	Proto       string    `json:"proto"`
	DNSMs       float64   `json:"dns_ms"`
	ConnectMs   float64   `json:"connect_ms"`
	TLSMs       float64   `json:"tls_ms"`
	TTFBMs      float64   `json:"ttfb_ms"`
	TotalMs     float64   `json:"total_ms"`
	IsWarmup    bool      `json:"is_warmup"`
	ErrorClass  string    `json:"error_class,omitempty"`
	ErrorMsg    string    `json:"error_msg,omitempty"`
}

// ProbeClient executes traced HTTP requests without leaking credentials.
type ProbeClient struct {
	client     *http.Client
	httpClientNoRedirect *http.Client
	baseURL    string
	runID      string
	emailSeq   uint64
}

func NewProbeClient(baseURL, runID string, timeout time.Duration) *ProbeClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		DisableKeepAlives:   false,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	noRedirectTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		DisableKeepAlives:   false,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	noRedirectClient := &http.Client{
		Transport: noRedirectTransport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &ProbeClient{
		client:               client,
		httpClientNoRedirect: noRedirectClient,
		baseURL:              strings.TrimRight(baseURL, "/"),
		runID:                runID,
		emailSeq:             uint64(time.Now().Unix() % 10000000) * 1000,
	}
}

// GenerateRegistrationEmail produces unique, audit-compliant email addresses.
func (p *ProbeClient) GenerateRegistrationEmail() (string, uint64) {
	seq := atomic.AddUint64(&p.emailSeq, 1)
	email := fmt.Sprintf("opsi-latency-%s-%06d@example.invalid", p.runID, seq)
	return email, seq
}

// ClassifyError classifies network and protocol errors into standard categories.
func ClassifyError(err error, statusCode int) string {
	if err == nil {
		if statusCode >= 200 && statusCode < 400 {
			return ""
		}
		if statusCode >= 400 && statusCode < 500 {
			return "http_4xx"
		}
		if statusCode >= 500 {
			return "http_5xx"
		}
		return "http_other"
	}

	errStr := strings.ToLower(err.Error())
	switch {
	case strings.Contains(errStr, "context canceled"):
		return "canceled"
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
		return "timeout"
	case strings.Contains(errStr, "no such host") || strings.Contains(errStr, "dns"):
		return "dns_error"
	case strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "dial tcp"):
		return "connect_error"
	case strings.Contains(errStr, "tls") || strings.Contains(errStr, "certificate") || strings.Contains(errStr, "handshake"):
		return "tls_error"
	default:
		return "transport_error"
	}
}

// RedactErrorMessage strips potential sensitive substrings (passwords, tokens) from error strings.
func RedactErrorMessage(msg string) string {
	if msg == "" {
		return ""
	}
	// Redact bearer tokens or passwords if accidentally present in error messages
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "bearer") || strings.Contains(lower, "password") || strings.Contains(lower, "token") {
		return "[REDACTED_ERROR_MESSAGE]"
	}
	// Limit length
	if len(msg) > 200 {
		return msg[:197] + "..."
	}
	return msg
}

// executeTracedRequest performs an HTTP call with httptrace timing metrics.
func (p *ProbeClient) executeTracedRequest(ctx context.Context, client *http.Client, req *http.Request) (RequestMetric, []byte, error) {
	var (
		dnsStart, dnsEnd       time.Time
		connStart, connEnd     time.Time
		tlsStart, tlsEnd       time.Time
		firstByteTime          time.Time
		totalStart             time.Time
		gotFirstByte           bool
	)

	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			dnsEnd = time.Now()
		},
		ConnectStart: func(network, addr string) {
			connStart = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			connEnd = time.Now()
		},
		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			tlsEnd = time.Now()
		},
		GotFirstResponseByte: func() {
			firstByteTime = time.Now()
			gotFirstByte = true
		},
	}

	tracedReq := req.WithContext(httptrace.WithClientTrace(ctx, trace))

	totalStart = time.Now()
	resp, err := client.Do(tracedReq)
	totalEnd := time.Now()

	metric := RequestMetric{
		Timestamp: totalStart.UTC(),
		Method:    req.Method,
		URL:       req.URL.String(),
	}

	var bodyBytes []byte
	if err != nil {
		metric.ErrorClass = ClassifyError(err, 0)
		metric.ErrorMsg = RedactErrorMessage(err.Error())
		metric.TotalMs = float64(totalEnd.Sub(totalStart).Microseconds()) / 1000.0
		return metric, nil, err
	}
	defer resp.Body.Close()

	// Read body to complete round-trip timing
	bodyBytes, readErr := io.ReadAll(resp.Body)
	bodyReadEnd := time.Now()

	metric.StatusCode = resp.StatusCode
	metric.Proto = resp.Proto

	// Calculate granular intervals
	if !dnsStart.IsZero() && !dnsEnd.IsZero() {
		metric.DNSMs = float64(dnsEnd.Sub(dnsStart).Microseconds()) / 1000.0
	}
	if !connStart.IsZero() && !connEnd.IsZero() {
		metric.ConnectMs = float64(connEnd.Sub(connStart).Microseconds()) / 1000.0
	}
	if !tlsStart.IsZero() && !tlsEnd.IsZero() {
		metric.TLSMs = float64(tlsEnd.Sub(tlsStart).Microseconds()) / 1000.0
	}
	if gotFirstByte {
		metric.TTFBMs = float64(firstByteTime.Sub(totalStart).Microseconds()) / 1000.0
	} else {
		// Fallback if hook didn't fire (e.g., cached / direct write)
		metric.TTFBMs = float64(totalEnd.Sub(totalStart).Microseconds()) / 1000.0
	}
	metric.TotalMs = float64(bodyReadEnd.Sub(totalStart).Microseconds()) / 1000.0

	if readErr != nil {
		metric.ErrorClass = "body_read_error"
		metric.ErrorMsg = RedactErrorMessage(readErr.Error())
		return metric, bodyBytes, readErr
	}

	metric.ErrorClass = ClassifyError(nil, resp.StatusCode)
	return metric, bodyBytes, nil
}

// ProbeRoot executes GET / with redirect measurement.
// Returns two metrics: step 1 (redirect response) and step 2 (target page), plus a composite total.
func (p *ProbeClient) ProbeRoot(ctx context.Context) ([]RequestMetric, error) {
	var metrics []RequestMetric
	startTotal := time.Now()

	// Step 1: GET / (no auto-redirect)
	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/", nil)
	if err != nil {
		return nil, err
	}
	req1.Header.Set("User-Agent", "Opsi-Latency-Probe/1.0")
	req1.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	m1, _, err1 := p.executeTracedRequest(ctx, p.httpClientNoRedirect, req1)
	m1.Scenario = "get_root"
	m1.Step = "redirect_step_1"
	metrics = append(metrics, m1)

	if err1 != nil {
		return metrics, err1
	}

	targetURL := p.baseURL + "/login"
	if m1.StatusCode >= 300 && m1.StatusCode < 400 {
		// Check Location header if available from step 1
		// For our app, GET / returns 307 Location: /login
		targetURL = p.baseURL + "/login"
	}

	// Step 2: GET target (e.g. /login)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return metrics, err
	}
	req2.Header.Set("User-Agent", "Opsi-Latency-Probe/1.0")
	req2.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	m2, _, err2 := p.executeTracedRequest(ctx, p.client, req2)
	m2.Scenario = "get_root"
	m2.Step = "redirect_step_2"
	metrics = append(metrics, m2)

	// Composite metric representing full user experience
	endTotal := time.Now()
	comp := RequestMetric{
		Timestamp:   m1.Timestamp,
		Scenario:    "get_root",
		Step:        "composite_total",
		Method:      "GET",
		URL:         p.baseURL + "/",
		StatusCode:  m2.StatusCode,
		Proto:       m2.Proto,
		DNSMs:       m1.DNSMs + m2.DNSMs,
		ConnectMs:   m1.ConnectMs + m2.ConnectMs,
		TLSMs:       m1.TLSMs + m2.TLSMs,
		TTFBMs:      m1.TTFBMs, // initial TTFB from first step
		TotalMs:     float64(endTotal.Sub(startTotal).Microseconds()) / 1000.0,
		ErrorClass:  m2.ErrorClass,
	}
	if err2 != nil {
		comp.ErrorClass = m2.ErrorClass
		comp.ErrorMsg = m2.ErrorMsg
	}
	metrics = append(metrics, comp)

	return metrics, err2
}

// ProbeLogin executes POST /api/auth/login with seed credentials.
func (p *ProbeClient) ProbeLogin(ctx context.Context, email, password string) (RequestMetric, error) {
	payload := map[string]string{
		"Email":    email,
		"Password": password,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return RequestMetric{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return RequestMetric{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Opsi-Latency-Probe/1.0")

	m, bodyResp, err := p.executeTracedRequest(ctx, p.client, req)
	m.Scenario = "post_login"
	m.Step = "single"

	if err != nil {
		return m, err
	}

	// Validate login JSON response format without saving secrets
	var respData struct {
		AccessToken string `json:"accessToken"`
	}
	if m.StatusCode == http.StatusOK {
		if jsonErr := json.Unmarshal(bodyResp, &respData); jsonErr != nil || respData.AccessToken == "" {
			m.ErrorClass = "contract_violation"
			m.ErrorMsg = "login 200 response missing accessToken"
			return m, fmt.Errorf("login contract violation: missing accessToken")
		}
	}

	return m, nil
}

// ProbeRegister executes POST /api/auth/register with unique email and sequence.
func (p *ProbeClient) ProbeRegister(ctx context.Context, password string) (RequestMetric, error) {
	email, seq := p.GenerateRegistrationEmail()
	payload := map[string]string{
		"Email":       email,
		"Password":    password,
		"DisplayName": fmt.Sprintf("Latency Probe %d", seq),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return RequestMetric{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/auth/register", bytes.NewReader(body))
	if err != nil {
		return RequestMetric{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Opsi-Latency-Probe/1.0")

	m, bodyResp, err := p.executeTracedRequest(ctx, p.client, req)
	m.Scenario = "post_register"
	m.Step = "single"
	m.Sequence = seq

	if err != nil {
		return m, err
	}

	// Validate registration JSON response format without saving secrets
	var respData struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if m.StatusCode == http.StatusOK || m.StatusCode == http.StatusCreated {
		if jsonErr := json.Unmarshal(bodyResp, &respData); jsonErr != nil || respData.ID == "" {
			m.ErrorClass = "contract_violation"
			m.ErrorMsg = "register 2xx response missing id"
			return m, fmt.Errorf("register contract violation: missing id")
		}
	}

	return m, nil
}
