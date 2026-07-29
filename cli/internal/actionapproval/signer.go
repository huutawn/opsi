package actionapproval

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func GenerateDevice() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func Sign(challenge actionv1.ApprovalChallenge, deviceID string, privateKey ed25519.PrivateKey) (actionv1.ApprovalGrant, error) {
	if len(privateKey) != ed25519.PrivateKeySize || deviceID == "" {
		return actionv1.ApprovalGrant{}, errors.New("secure ActionPlane device key is invalid")
	}
	if err := challenge.Validate(time.Now().UTC()); err != nil {
		return actionv1.ApprovalGrant{}, err
	}
	body, err := CanonicalApproval(challenge, deviceID)
	if err != nil {
		return actionv1.ApprovalGrant{}, err
	}
	return actionv1.ApprovalGrant{SchemaVersion: actionv1.SchemaVersion, ChallengeID: challenge.ID, ActionID: challenge.ActionID, ProjectID: challenge.ProjectID, DeviceID: deviceID, PlanHash: challenge.PlanHash, StateHash: challenge.StateHash, Nonce: challenge.Nonce, IssuedAt: challenge.IssuedAt, ExpiresAt: challenge.ExpiresAt, Signature: ed25519.Sign(privateKey, body)}, nil
}
