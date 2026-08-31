// Package cloudflare contains the bounded Cloudflare DNS and zone-rules
// provisioner used by public hostname publication.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAPIURL = "https://api.cloudflare.com/client/v4"

var ErrDNSConflict = errors.New("cloudflare DNS record conflicts with Opsi allocation")
var ErrOwnership = errors.New("cloudflare DNS record ownership could not be verified")

type Client struct {
	zoneID   string
	token    string
	domain   string
	baseURL  string
	http     *http.Client
	rulesMu  sync.Mutex
	maxTries int
}

type Options struct {
	ZoneID     string
	APIToken   string
	Domain     string
	APIURL     string
	HTTPClient *http.Client
}

type Record struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Proxied bool     `json:"proxied"`
	TTL     int      `json:"ttl"`
	Comment string   `json:"comment"`
	Tags    []string `json:"tags"`
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.ZoneID) == "" || strings.TrimSpace(options.APIToken) == "" || strings.TrimSpace(options.Domain) == "" {
		return nil, errors.New("cloudflare zone id, API token, and deployment domain are required")
	}
	baseURL := strings.TrimRight(options.APIURL, "/")
	if baseURL == "" {
		baseURL = defaultAPIURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")) {
		return nil, errors.New("cloudflare API URL must use HTTPS or loopback HTTP")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	clone := *httpClient
	if clone.Timeout <= 0 || clone.Timeout > 30*time.Second {
		clone.Timeout = 10 * time.Second
	}
	if clone.CheckRedirect == nil {
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpClient = &clone
	return &Client{zoneID: strings.TrimSpace(options.ZoneID), token: strings.TrimSpace(options.APIToken), domain: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(options.Domain)), "."), baseURL: baseURL, http: httpClient, maxTries: 3}, nil
}

func Marker(allocationID string) string { return "opsi-public-hostname:" + allocationID }

func (c *Client) ValidateZone(ctx context.Context) error {
	var zone struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID), nil, &zone); err != nil {
		return fmt.Errorf("validate Cloudflare zone: %w", err)
	}
	zoneName := strings.TrimSuffix(strings.ToLower(zone.Name), ".")
	if zone.ID != c.zoneID || zoneName == "" || c.domain != zoneName && !strings.HasSuffix(c.domain, "."+zoneName) {
		return errors.New("Cloudflare zone does not own the deployment domain suffix")
	}
	return nil
}

func (c *Client) ReconcileARecord(ctx context.Context, hostname, ipv4, allocationID string) (Record, error) {
	parsed := net.ParseIP(ipv4)
	if parsed == nil || parsed.To4() == nil {
		return Record{}, errors.New("target public_host must be an IPv4 address")
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if !strings.HasSuffix(hostname, "."+c.domain) {
		return Record{}, errors.New("hostname is outside the managed deployment domain")
	}
	var records []Record
	query := "?name=" + url.QueryEscape(hostname)
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records"+query, nil, &records); err != nil {
		return Record{}, err
	}
	marker := Marker(allocationID)
	if len(records) > 1 {
		return Record{}, ErrDNSConflict
	}
	payload := map[string]any{"type": "A", "name": hostname, "content": parsed.To4().String(), "proxied": true, "ttl": 1, "comment": marker, "tags": []string{marker}}
	if len(records) == 0 {
		var created Record
		if err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records", payload, &created); err != nil {
			return Record{}, err
		}
		return created, nil
	}
	record := records[0]
	if record.Type != "A" || !owned(record, allocationID) {
		return Record{}, ErrDNSConflict
	}
	if record.Content == parsed.To4().String() && record.Proxied && record.TTL == 1 {
		return record, nil
	}
	var updated Record
	if err := c.do(ctx, http.MethodPatch, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records/"+url.PathEscape(record.ID), payload, &updated); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (c *Client) DeleteARecord(ctx context.Context, recordID, hostname, allocationID string) error {
	if recordID == "" {
		record, found, err := c.ownedRecordByHostname(ctx, hostname, allocationID)
		if err != nil || !found {
			return err
		}
		recordID = record.ID
	}
	var record Record
	err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, &record)
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Type != "A" || !strings.EqualFold(record.Name, hostname) || !owned(record, allocationID) {
		return ErrOwnership
	}
	return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil)
}

// ownedRecordByHostname recovers an Opsi record only through its allocation
// marker. It makes a release safe even if Cloudflare created the record just
// before a transient database failure prevented its ID from being persisted.
func (c *Client) ownedRecordByHostname(ctx context.Context, hostname, allocationID string) (Record, bool, error) {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	var records []Record
	query := "?name=" + url.QueryEscape(hostname)
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/dns_records"+query, nil, &records); err != nil {
		return Record{}, false, err
	}
	for _, record := range records {
		if record.Type == "A" && strings.EqualFold(record.Name, hostname) && owned(record, allocationID) {
			return record, true, nil
		}
	}
	return Record{}, false, nil
}

func owned(record Record, allocationID string) bool {
	marker := Marker(allocationID)
	if record.Comment == marker {
		return true
	}
	for _, tag := range record.Tags {
		if tag == marker {
			return true
		}
	}
	return false
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e APIError) Error() string {
	return fmt.Sprintf("Cloudflare API request failed (status %d, code %d): %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, result any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < c.maxTries; attempt++ {
		req, requestErr := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
		if requestErr != nil {
			return requestErr
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, requestErr := c.http.Do(req)
		if requestErr != nil {
			if attempt+1 < c.maxTries && ctx.Err() == nil {
				if waitErr := wait(ctx, time.Duration(attempt+1)*100*time.Millisecond); waitErr != nil {
					return waitErr
				}
				continue
			}
			return errors.New("Cloudflare API request failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return errors.New("Cloudflare API response could not be read")
		}
		var payload envelope
		_ = json.Unmarshal(data, &payload)
		if response.StatusCode >= 200 && response.StatusCode < 300 && payload.Success {
			if result != nil && len(payload.Result) > 0 {
				if err := json.Unmarshal(payload.Result, result); err != nil {
					return errors.New("Cloudflare API response was invalid")
				}
			}
			return nil
		}
		message := "request rejected"
		code := 0
		if len(payload.Errors) > 0 {
			code = payload.Errors[0].Code
			message = safeMessage(payload.Errors[0].Message)
		}
		apiErr := APIError{Status: response.StatusCode, Code: code, Message: message}
		if attempt+1 < c.maxTries && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) {
			delay := retryDelay(response.Header.Get("Retry-After"), attempt)
			if err := wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
	return errors.New("Cloudflare API retry limit reached")
}

func safeMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	if value == "" {
		return "request rejected"
	}
	return value
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > 5*time.Second {
			return 5 * time.Second
		}
		return delay
	}
	return time.Duration(attempt+1) * 200 * time.Millisecond
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
