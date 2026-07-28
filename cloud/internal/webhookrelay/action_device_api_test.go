package webhookrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/actiondevice"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
)

func TestActionDeviceAPIUsesAuthenticatedActorAndHidesPrivateOrRawPublicKey(t *testing.T) {
	server := NewServer(Config{})
	server.SetActionDeviceStore(actiondevice.NewMemoryStore())
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{ID: "pat-1", UserID: "owner-1", ProjectID: "p1", OrgID: "o1", Role: "owner", Hash: actionDeviceHash(t, "pat-1"), ExpiresAt: time.Now().Add(time.Hour)}}}}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	body, _ := json.Marshal(map[string]any{"display_name": "laptop", "public_key": base64.StdEncoding.EncodeToString(publicKey), "idempotency_key": "one", "owner_principal": "spoof"})
	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/action-devices", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer pat-1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var device actiondevice.Device
	if err := json.NewDecoder(response.Body).Decode(&device); err != nil {
		t.Fatal(err)
	}
	if device.OwnerPrincipal != "owner-1" || device.TrustedActor != "owner-1" || len(device.PublicKey) != 0 || device.FingerprintSHA256 == "" {
		t.Fatalf("unsafe device response: %#v", device)
	}
}

func TestActionDeviceAPIViewerDeniedAndRevokeIdempotent(t *testing.T) {
	server := NewServer(Config{})
	store := actiondevice.NewMemoryStore()
	server.SetActionDeviceStore(store)
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{ID: "owner-pat", UserID: "owner", ProjectID: "p1", OrgID: "o1", Role: "owner", Hash: actionDeviceHash(t, "owner-pat"), ExpiresAt: time.Now().Add(time.Hour)}, {ID: "viewer-pat", UserID: "viewer", ProjectID: "p1", OrgID: "o1", Role: "viewer", Hash: actionDeviceHash(t, "viewer-pat"), ExpiresAt: time.Now().Add(time.Hour)}}}}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/p1/action-devices", bytes.NewReader(mustJSON(t, map[string]any{"display_name": "laptop", "public_key": base64.StdEncoding.EncodeToString(publicKey), "idempotency_key": "one"})))
	request.Header.Set("Authorization", "Bearer owner-pat")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	var device actiondevice.Device
	if err := json.NewDecoder(response.Body).Decode(&device); err != nil {
		t.Fatal(err)
	}
	viewer := httptest.NewRequest(http.MethodGet, "/api/projects/p1/action-devices", nil)
	viewer.Header.Set("Authorization", "Bearer viewer-pat")
	viewerResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(viewerResponse, viewer)
	if viewerResponse.Code != http.StatusOK {
		t.Fatalf("viewer list status=%d", viewerResponse.Code)
	}
	revoke := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/action-devices/"+device.ID+"/revoke", nil)
		req.Header.Set("Authorization", "Bearer owner-pat")
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, req)
		return out
	}
	if first := revoke(); first.Code != http.StatusOK {
		t.Fatalf("first revoke status=%d body=%s", first.Code, first.Body.String())
	}
	if second := revoke(); second.Code != http.StatusOK {
		t.Fatalf("second revoke status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestAgentActionDeviceLookupReturnsOnlyActiveProjectDevice(t *testing.T) {
	server := NewServer(Config{})
	server.Registry = &deploymentResultRegistry{}
	store := actiondevice.NewMemoryStore()
	server.SetActionDeviceStore(store)
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	service := actiondevice.Service{Store: store}
	device, _, err := service.Register(context.Background(), actiondevice.Principal{ProjectID: "p1", UserID: "owner", Role: "owner"}, actiondevice.RegisterRequest{DisplayName: "laptop", PublicKey: publicKey, IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(projectID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/v1/agent/projects/"+projectID+"/action-devices/"+device.ID, nil)
		request.Header.Set("Authorization", "Bearer agent-token")
		request.Header.Set("X-Opsi-Node-ID", "node-1")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	if response := lookup("p1"); response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(base64.StdEncoding.EncodeToString(publicKey))) {
		t.Fatalf("active lookup status=%d body=%s", response.Code, response.Body.String())
	}
	if response := lookup("other"); response.Code != http.StatusNotFound {
		t.Fatalf("cross-project lookup status=%d body=%s", response.Code, response.Body.String())
	}
	if _, _, err := service.Revoke(context.Background(), actiondevice.Principal{ProjectID: "p1", UserID: "owner", Role: "owner"}, device.ID); err != nil {
		t.Fatal(err)
	}
	if response := lookup("p1"); response.Code != http.StatusNotFound {
		t.Fatalf("revoked lookup status=%d body=%s", response.Code, response.Body.String())
	}
}

func actionDeviceHash(t *testing.T, token string) string {
	t.Helper()
	hash, err := auth.HashPAT(token)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
