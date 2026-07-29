package secret

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAuthVerifierUsesBearerAndProjectOnlyBody(t *testing.T) {
	const pat = "pat-secret-canary"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+pat {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), pat) {
			t.Fatalf("request body contains PAT: %s", body)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 1 || payload["project_id"] != "project-1" {
			t.Fatalf("payload = %#v", payload)
		}
		_, _ = w.Write([]byte(`{"user_id":"user-1","project_id":"project-1","role":"owner"}`))
	}))
	defer server.Close()

	verifier := &HTTPAuthVerifier{Endpoint: server.URL}
	verified, err := verifier.VerifyAuth(context.Background(), AuthContext{ProjectID: "project-1", PAT: pat})
	if err != nil {
		t.Fatal(err)
	}
	if verified.UserID != "user-1" || verified.ProjectID != "project-1" || verified.Role != RoleOwner || verified.PAT != pat {
		t.Fatalf("verified = %+v", verified)
	}
	for key, entry := range verifier.cache {
		if strings.Contains(key, pat) || entry.Auth.PAT != "" {
			t.Fatalf("cache retained plaintext PAT: key=%q auth=%+v", key, entry.Auth)
		}
	}
}

func TestHTTPAuthVerifierRejectsProjectMismatchAndMissingIdentity(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "project mismatch", body: `{"user_id":"user-1","project_id":"project-2","role":"Owner"}`},
		{name: "missing user", body: `{"project_id":"project-1","role":"Owner"}`},
		{name: "missing project", body: `{"user_id":"user-1","role":"Owner"}`},
		{name: "missing role", body: `{"user_id":"user-1","project_id":"project-1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := (&HTTPAuthVerifier{Endpoint: server.URL}).VerifyAuth(context.Background(), AuthContext{ProjectID: "project-1", PAT: "pat-secret"})
			if err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}

func TestHTTPAuthVerifierExpiryBoundaryFailsClosed(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"user_id":"user-1","project_id":"project-1","role":"Owner"}`))
			return
		}
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer server.Close()

	verifier := &HTTPAuthVerifier{Endpoint: server.URL, CacheTTL: time.Minute, Now: func() time.Time { return now }}
	if _, err := verifier.VerifyAuth(context.Background(), AuthContext{ProjectID: "project-1", PAT: "pat-secret"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := verifier.VerifyAuth(context.Background(), AuthContext{ProjectID: "project-1", PAT: "pat-secret"}); err == nil {
		t.Fatal("cache entry remained valid at expires_at")
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestHTTPAuthVerifierErrorsDoNotContainPAT(t *testing.T) {
	const pat = "pat-error-canary"
	tests := []struct {
		name   string
		client *http.Client
	}{
		{name: "non-2xx"},
		{name: "transport", client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport unavailable")
		})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}))
			defer server.Close()
			_, err := (&HTTPAuthVerifier{Endpoint: server.URL, Client: tt.client}).VerifyAuth(context.Background(), AuthContext{ProjectID: "project-1", PAT: pat})
			if err == nil || strings.Contains(err.Error(), pat) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
