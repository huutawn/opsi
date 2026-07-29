package actionplane

import (
	"context"
	"testing"
)

func TestHTTPDeviceResolverFailsClosedOnMalformedOrUnavailableCloud(t *testing.T) {
	resolver := HTTPDeviceResolver{BaseURL: "http://127.0.0.1:1", Token: "agent-token", NodeID: "node-1"}
	if _, err := resolver.Resolve(context.Background(), "p1", "device-1", "u1"); err == nil {
		t.Fatal("unavailable cloud resolver succeeded")
	}
}
