package webhookrelay

import (
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
)

func TestPlacementHeartbeatThresholdIsServerSideAndBounded(t *testing.T) {
	for _, ttl := range []time.Duration{29 * time.Second, 31 * time.Minute} {
		cfg := Config{GitHubOIDC: githuboidc.DefaultConfig(), Placement: PlacementConfig{HeartbeatTTL: Duration(ttl)}}
		if err := validateConfig(&cfg); err == nil {
			t.Fatalf("ttl %s accepted", ttl)
		}
	}
	cfg := Config{GitHubOIDC: githuboidc.DefaultConfig(), Placement: PlacementConfig{HeartbeatTTL: Duration(90 * time.Second), ReservedCPUMilli: 250, ReservedMemoryBytes: 256 << 20}}
	if err := validateConfig(&cfg); err != nil {
		t.Fatal(err)
	}
}
