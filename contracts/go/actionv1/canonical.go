package actionv1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

func DecodeStrict(data []byte, target any) error {
	if len(data) > MaxJSONBytes {
		return errors.New("JSON body exceeds the allowed bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON body must contain one value")
	}
	return nil
}

func CanonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

func PlanHash(plan ActionPlan) (string, error) {
	plan.PlanHash = ""
	body, err := CanonicalJSON(plan)
	if err != nil {
		return "", err
	}
	return hash(body), nil
}

func StateHash(state CurrentState) (string, error) {
	state.StateHash = ""
	body, err := CanonicalJSON(state)
	if err != nil {
		return "", err
	}
	return hash(body), nil
}

type signedChallenge struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	ActionID      string `json:"action_id"`
	ProjectID     string `json:"project_id"`
	PlanHash      string `json:"plan_hash"`
	StateHash     string `json:"state_hash"`
	Nonce         string `json:"nonce"`
	IssuedAt      int64  `json:"issued_at_unix_nano"`
	ExpiresAt     int64  `json:"expires_at_unix_nano"`
}

func ChallengeSigningBytes(challenge ApprovalChallenge) ([]byte, error) {
	return CanonicalJSON(signedChallenge{SchemaVersion: challenge.SchemaVersion, ID: challenge.ID, ActionID: challenge.ActionID, ProjectID: challenge.ProjectID, PlanHash: challenge.PlanHash, StateHash: challenge.StateHash, Nonce: challenge.Nonce, IssuedAt: challenge.IssuedAt.UnixNano(), ExpiresAt: challenge.ExpiresAt.UnixNano()})
}

func ApprovalSigningBytes(challenge ApprovalChallenge, deviceID string) ([]byte, error) {
	challengeBytes, err := ChallengeSigningBytes(challenge)
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(struct {
		Challenge json.RawMessage `json:"challenge"`
		DeviceID  string          `json:"device_id"`
	}{Challenge: challengeBytes, DeviceID: deviceID})
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
