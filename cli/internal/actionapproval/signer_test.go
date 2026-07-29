package actionapproval

import (
	"crypto/ed25519"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestSignerCreatesVerifiableGrantWithoutChangingChallenge(t *testing.T) {
	publicKey, privateKey, err := GenerateDevice()
	if err != nil {
		t.Fatal(err)
	}
	challenge := actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "c1", ActionID: "a1", ProjectID: "p1", PlanHash: hash64('a'), StateHash: hash64('b'), Nonce: "n1", IssuedAt: time.Unix(1_800_000_000, 0).UTC(), ExpiresAt: time.Unix(1_800_000_060, 0).UTC()}
	grant, err := Sign(challenge, "device-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := actionv1.ApprovalSigningBytes(challenge, "device-1")
	if !ed25519.Verify(publicKey, bytes, grant.Signature) || grant.Nonce != challenge.Nonce || grant.DeviceID != "device-1" {
		t.Fatalf("invalid grant: %#v", grant)
	}
}

func hash64(value byte) string {
	data := make([]byte, 64)
	for i := range data {
		data[i] = value
	}
	return string(data)
}
