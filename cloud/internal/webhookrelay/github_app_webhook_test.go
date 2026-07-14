package webhookrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type githubAppSinkFunc func(context.Context, GitHubAppEvent) error

func (f githubAppSinkFunc) HandleGitHubAppEvent(ctx context.Context, event GitHubAppEvent) error {
	return f(ctx, event)
}

func newGitHubAppWebhookServer(secret string) *Server {
	return NewServer(Config{GitHubApp: GitHubAppConfig{WebhookSecret: secret}})
}

func githubAppWebhookRequest(method, event, delivery string, body []byte, secret string) *http.Request {
	request := httptest.NewRequest(method, "/v1/webhooks/github-app", bytes.NewReader(body))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}

func serveGitHubAppWebhook(server *Server, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func installationPayload(action string) []byte {
	return []byte(fmt.Sprintf(`{"action":%q,"installation":{"id":101,"account":{"id":202,"login":"example","type":"Organization"}},"remote_secret":"must-not-persist"}`, action))
}

func repositoryPayload(action string, repositoryID int64) []byte {
	return []byte(fmt.Sprintf(`{"action":%q,"installation":{"id":101},"repository":{"id":%d,"node_id":"R_1","name":"api","full_name":"example/api","private":true,"archived":false,"disabled":false,"default_branch":"main","owner":{"id":202,"login":"example"}}}`, action, repositoryID))
}

func TestGitHubAppWebhookSignatureVerification(t *testing.T) {
	secret := strings.Repeat("w", 32)
	ping := []byte(`{"zen":"remote controlled","hook_id":1,"installation":{"id":101}}`)
	t.Run("valid SHA-256", func(t *testing.T) {
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodPost, "ping", "delivery-valid", ping, secret))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("invalid", func(t *testing.T) {
		request := githubAppWebhookRequest(http.MethodPost, "ping", "delivery-invalid", ping, secret)
		request.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("missing", func(t *testing.T) {
		request := githubAppWebhookRequest(http.MethodPost, "ping", "delivery-missing", ping, secret)
		request.Header.Del("X-Hub-Signature-256")
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("legacy SHA-1 only", func(t *testing.T) {
		request := githubAppWebhookRequest(http.MethodPost, "ping", "delivery-sha1", ping, secret)
		request.Header.Del("X-Hub-Signature-256")
		mac := hmac.New(sha1.New, []byte(secret))
		_, _ = mac.Write(ping)
		request.Header.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("modified body", func(t *testing.T) {
		request := githubAppWebhookRequest(http.MethodPost, "ping", "delivery-modified", ping, secret)
		request.Body = ioNopCloser(strings.NewReader(`{"hook_id":2}`))
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("signature before JSON", func(t *testing.T) {
		request := githubAppWebhookRequest(http.MethodPost, "installation", "delivery-order", []byte("invalid-json"), secret)
		request.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("body limit", func(t *testing.T) {
		body := bytes.Repeat([]byte("x"), githubAppWebhookMaxBytes+1)
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodPost, "ping", "delivery-large", body, secret))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d", response.Code)
		}
	})
}

// ioNopCloser avoids importing io solely for one modified-body assertion.
func ioNopCloser(reader *strings.Reader) *readCloser { return &readCloser{Reader: reader} }

type readCloser struct{ *strings.Reader }

func (*readCloser) Close() error { return nil }

func TestGitHubAppWebhookParsesTypedEvents(t *testing.T) {
	secret := strings.Repeat("w", 32)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		eventName  string
		body       []byte
		assertions func(*testing.T, GitHubAppEvent)
	}{
		{name: "installation created", eventName: "installation", body: installationPayload("created"), assertions: func(t *testing.T, event GitHubAppEvent) {
			if event.Action != "created" || event.InstallationID != 101 || event.AccountID != 202 || event.AccountLogin != "example" || event.AccountType != "Organization" {
				t.Fatalf("event=%+v", event)
			}
		}},
		{name: "installation deleted", eventName: "installation", body: installationPayload("deleted"), assertions: func(t *testing.T, event GitHubAppEvent) {
			if event.Action != "deleted" || event.InstallationID != 101 || event.AccountID != 202 {
				t.Fatalf("event=%+v", event)
			}
		}},
		{name: "repositories added", eventName: "installation_repositories", body: []byte(`{"action":"added","installation":{"id":101},"repositories_added":[{"id":301,"name":"api","full_name":"example/api","owner":{"id":202,"login":"example"}}],"repositories_removed":[]}`), assertions: func(t *testing.T, event GitHubAppEvent) {
			if event.InstallationID != 101 || len(event.Added) != 1 || event.Added[0].ID != 301 || event.Added[0].OwnerID != 202 || len(event.Removed) != 0 {
				t.Fatalf("event=%+v", event)
			}
		}},
		{name: "repositories removed", eventName: "installation_repositories", body: []byte(`{"action":"removed","installation":{"id":101},"repositories_added":[],"repositories_removed":[{"id":302,"name":"old","full_name":"example/old"}]}`), assertions: func(t *testing.T, event GitHubAppEvent) {
			if len(event.Removed) != 1 || event.Removed[0].ID != 302 || len(event.Added) != 0 {
				t.Fatalf("event=%+v", event)
			}
		}},
		{name: "repository renamed", eventName: "repository", body: repositoryPayload("renamed", 401), assertions: func(t *testing.T, event GitHubAppEvent) {
			if event.Repository == nil || event.Repository.ID != 401 || event.Repository.FullName != "example/api" || event.InstallationID != 101 {
				t.Fatalf("event=%+v", event)
			}
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newGitHubAppWebhookServer(secret)
			server.now = func() time.Time { return now }
			var received GitHubAppEvent
			server.SetGitHubAppEventSink(githubAppSinkFunc(func(_ context.Context, event GitHubAppEvent) error {
				received = event
				return nil
			}))
			request := githubAppWebhookRequest(http.MethodPost, test.eventName, fmt.Sprintf("delivery-%d", index), test.body, secret)
			response := serveGitHubAppWebhook(server, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if received.DeliveryID == "" || received.Event != test.eventName || !received.ReceivedAt.Equal(now) {
				t.Fatalf("event metadata=%+v", received)
			}
			test.assertions(t, received)
			serialized, _ := json.Marshal(received)
			if strings.Contains(string(serialized), "must-not-persist") {
				t.Fatalf("typed event retained raw body: %s", serialized)
			}
		})
	}
}

func TestGitHubAppWebhookIgnoresUnknownEventAndAction(t *testing.T) {
	secret := strings.Repeat("w", 32)
	var calls atomic.Int32
	server := newGitHubAppWebhookServer(secret)
	server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { calls.Add(1); return nil }))
	for _, request := range []*http.Request{
		githubAppWebhookRequest(http.MethodPost, "future_event", "delivery-event", []byte("not-json"), secret),
		githubAppWebhookRequest(http.MethodPost, "installation", "delivery-action", []byte(`{"action":"future_action"}`), secret),
	} {
		response := serveGitHubAppWebhook(server, request)
		if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "ignored") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("sink calls=%d", calls.Load())
	}
}

func TestGitHubAppWebhookRejectsMalformedSupportedPayloads(t *testing.T) {
	secret := strings.Repeat("w", 32)
	tests := map[string]struct {
		event string
		body  []byte
	}{
		"missing installation": {event: "installation", body: []byte(`{"action":"created"}`)},
		"repository ID zero":   {event: "repository", body: repositoryPayload("renamed", 0)},
		"duplicate repository": {event: "installation_repositories", body: []byte(`{"action":"added","installation":{"id":101},"repositories_added":[{"id":301},{"id":301}],"repositories_removed":[]}`)},
		"invalid JSON":         {event: "repository", body: []byte(`{"action":"renamed"`)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodPost, test.event, "delivery-"+strings.ReplaceAll(name, " ", "-"), test.body, secret))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestGitHubAppWebhookRequiresEventAndDeliveryHeaders(t *testing.T) {
	secret := strings.Repeat("w", 32)
	body := installationPayload("created")
	for _, header := range []string{"X-GitHub-Event", "X-GitHub-Delivery"} {
		request := githubAppWebhookRequest(http.MethodPost, "installation", "delivery-required", body, secret)
		request.Header.Del(header)
		response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing %s status=%d", header, response.Code)
		}
	}
}

func TestGitHubAppWebhookReplayProtection(t *testing.T) {
	secret := strings.Repeat("w", 32)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	t.Run("completed duplicate", func(t *testing.T) {
		server := newGitHubAppWebhookServer(secret)
		var calls atomic.Int32
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { calls.Add(1); return nil }))
		for index := range 2 {
			response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-completed", installationPayload("created"), secret))
			if index == 0 && response.Code != http.StatusOK || index == 1 && (response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"duplicate":true`)) {
				t.Fatalf("attempt=%d status=%d body=%s", index, response.Code, response.Body.String())
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("sink calls=%d", calls.Load())
		}
	})
	t.Run("in-flight duplicate", func(t *testing.T) {
		server := newGitHubAppWebhookServer(secret)
		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		}))
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			firstDone <- serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-flight", installationPayload("created"), secret))
		}()
		<-started
		second := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-flight", installationPayload("created"), secret))
		if second.Code != http.StatusConflict || calls.Load() != 1 {
			t.Fatalf("status=%d calls=%d", second.Code, calls.Load())
		}
		close(release)
		if first := <-firstDone; first.Code != http.StatusOK {
			t.Fatalf("first status=%d", first.Code)
		}
	})
	t.Run("failure releases reservation", func(t *testing.T) {
		server := newGitHubAppWebhookServer(secret)
		var calls atomic.Int32
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error {
			if calls.Add(1) == 1 {
				return errors.New("temporary failure")
			}
			return nil
		}))
		first := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-retry", installationPayload("created"), secret))
		second := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-retry", installationPayload("created"), secret))
		if first.Code != http.StatusInternalServerError || second.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("statuses=%d/%d calls=%d", first.Code, second.Code, calls.Load())
		}
	})
	t.Run("expired delivery", func(t *testing.T) {
		current := now
		server := newGitHubAppWebhookServer(secret)
		server.githubReplay = newGitHubReplayStore(10, githubReplayTTL, func() time.Time { return current })
		var calls atomic.Int32
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { calls.Add(1); return nil }))
		if response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-expired", installationPayload("created"), secret)); response.Code != http.StatusOK {
			t.Fatalf("first status=%d", response.Code)
		}
		current = current.Add(25 * time.Hour)
		if response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-expired", installationPayload("created"), secret)); response.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("retry status=%d calls=%d", response.Code, calls.Load())
		}
	})
	t.Run("store full", func(t *testing.T) {
		server := newGitHubAppWebhookServer(secret)
		server.githubReplay = newGitHubReplayStore(1, githubReplayTTL, func() time.Time { return now })
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { return nil }))
		_ = serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-one", installationPayload("created"), secret))
		response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-two", installationPayload("created"), secret))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", response.Code)
		}
	})
}

func TestGitHubAppWebhookNilSinkAndErrorMapping(t *testing.T) {
	secret := strings.Repeat("w", 32)
	server := newGitHubAppWebhookServer(secret)
	mutation := githubAppWebhookRequest(http.MethodPost, "installation", "delivery-nil", installationPayload("created"), secret)
	if response := serveGitHubAppWebhook(server, mutation); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil sink status=%d", response.Code)
	}
	var calls atomic.Int32
	server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { calls.Add(1); return nil }))
	if response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", "delivery-nil", installationPayload("created"), secret)); response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("retry status=%d calls=%d", response.Code, calls.Load())
	}
	for _, test := range []struct {
		err    error
		status int
	}{
		{err: ErrGitHubEventSinkUnavailable, status: http.StatusServiceUnavailable},
		{err: ErrGitHubEventConflict, status: http.StatusConflict},
		{err: errors.New("internal detail"), status: http.StatusInternalServerError},
	} {
		server := newGitHubAppWebhookServer(secret)
		server.SetGitHubAppEventSink(githubAppSinkFunc(func(context.Context, GitHubAppEvent) error { return test.err }))
		response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "installation", fmt.Sprintf("delivery-error-%d", test.status), installationPayload("created"), secret))
		if response.Code != test.status || strings.Contains(response.Body.String(), test.err.Error()) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	ping := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodPost, "ping", "delivery-ping", []byte(`{"hook_id":1}`), secret))
	ignored := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodPost, "future_event", "delivery-ignored", []byte(`{}`), secret))
	if ping.Code != http.StatusOK || ignored.Code != http.StatusAccepted {
		t.Fatalf("ping=%d ignored=%d", ping.Code, ignored.Code)
	}
}

func TestGitHubAppWebhookValidatesDeliveryAndReplayStateIsMetadataOnly(t *testing.T) {
	secret := strings.Repeat("w", 32)
	request := githubAppWebhookRequest(http.MethodPost, "installation", "bad\ndelivery", installationPayload("created"), secret)
	response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", response.Code)
	}
	typeOfEntry := reflect.TypeOf(githubReplayEntry{})
	if typeOfEntry.NumField() != 2 || typeOfEntry.Field(0).Name != "state" || typeOfEntry.Field(1).Name != "expiresAt" {
		t.Fatalf("replay entry fields changed: %v", typeOfEntry)
	}
}

func TestGitHubAppWebhookOnlyAcceptsPOST(t *testing.T) {
	secret := strings.Repeat("w", 32)
	response := serveGitHubAppWebhook(newGitHubAppWebhookServer(secret), githubAppWebhookRequest(http.MethodGet, "ping", "delivery-method", []byte(`{}`), secret))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestGitHubAppWebhookConcurrentSinkCaptureIsSafe(t *testing.T) {
	secret := strings.Repeat("w", 32)
	server := newGitHubAppWebhookServer(secret)
	var mu sync.Mutex
	seen := map[string]bool{}
	server.SetGitHubAppEventSink(githubAppSinkFunc(func(_ context.Context, event GitHubAppEvent) error {
		mu.Lock()
		seen[event.DeliveryID] = true
		mu.Unlock()
		return nil
	}))
	var wait sync.WaitGroup
	for index := range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			delivery := fmt.Sprintf("delivery-concurrent-%d", index)
			response := serveGitHubAppWebhook(server, githubAppWebhookRequest(http.MethodPost, "repository", delivery, repositoryPayload("edited", int64(index+1)), secret))
			if response.Code != http.StatusOK {
				t.Errorf("status=%d", response.Code)
			}
		}()
	}
	wait.Wait()
	if len(seen) != 4 {
		t.Fatalf("seen=%v", seen)
	}
}
