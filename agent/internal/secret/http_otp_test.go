package secret

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPOTPClientSendsPAT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-secret" {
			t.Fatalf("authorization header=%q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/otp/request" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"request_id": "otp-1"})
	}))
	defer server.Close()

	client := HTTPOTPClient{Endpoint: server.URL}
	requestID, err := client.RequestOTP(context.Background(), AuthContext{ProjectID: "proj-1", UserID: "user-1", PAT: "pat-secret"}, "secret.reveal", SecretRef{Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "otp-1" {
		t.Fatalf("request id=%q", requestID)
	}
}
