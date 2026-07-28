package actionapproval

import (
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestCanonicalApprovalBindsDeviceAndExpiry(t *testing.T) {
	challenge := actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "c", ActionID: "a", ProjectID: "p", PlanHash: hash64('a'), StateHash: hash64('b'), Nonce: "n", IssuedAt: time.Unix(1_800_000_000, 0).UTC(), ExpiresAt: time.Unix(1_800_000_060, 0).UTC()}
	first, err := CanonicalApproval(challenge, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := CanonicalApproval(challenge, "device-2")
	if string(first) == string(second) {
		t.Fatal("device identity did not change approval bytes")
	}
	challenge.ExpiresAt = challenge.ExpiresAt.Add(time.Second)
	third, _ := CanonicalApproval(challenge, "device-1")
	if string(first) == string(third) {
		t.Fatal("expiry did not change approval bytes")
	}
}
