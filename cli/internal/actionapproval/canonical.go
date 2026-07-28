package actionapproval

import actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"

func CanonicalApproval(challenge actionv1.ApprovalChallenge, deviceID string) ([]byte, error) {
	return actionv1.ApprovalSigningBytes(challenge, deviceID)
}
