package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	ActionCreate = "secret.create"
	ActionReveal = "secret.reveal"
	ActionRotate = "secret.rotate"
	ActionTOTP   = "secret.totp_setup"
)

type OTPClient interface {
	RequestOTP(ctx context.Context, auth AuthContext, purpose string, ref SecretRef) (string, error)
	VerifyOTP(ctx context.Context, auth AuthContext, requestID, purpose, code string) error
}

type EncryptionVerifier interface {
	Verify(ctx context.Context) error
}

type StaticEncryptionVerifier bool

func (v StaticEncryptionVerifier) Verify(context.Context) error {
	if !bool(v) {
		return errors.New("k3s encryption at rest is not confirmed")
	}
	return nil
}

type Service struct {
	Store            Store
	TOTPStore        TOTPStore
	Auth             AuthVerifier
	Audit            AuditSink
	OTP              OTPClient
	Encryption       EncryptionVerifier
	Restarter        RolloutRestarter
	CloudOTPTimeout  time.Duration
	TOTPSecretByUser map[string]string
	Now              func() time.Time
}

func (s *Service) SetupTOTP(ctx context.Context, auth AuthContext) (string, string, error) {
	verified, err := s.authorize(ctx, auth)
	if err != nil {
		_ = s.audit(ctx, auth, ActionTOTP, "totp", auth.UserID, "denied", map[string]string{"reason": "auth"})
		return "", "", err
	}
	auth = verified
	if auth.UserID == "" || auth.ProjectID == "" {
		return "", "", errors.New("user_id and project_id are required")
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		_ = s.audit(ctx, auth, ActionTOTP, "totp", auth.UserID, "failed", nil)
		return "", "", err
	}
	if s.TOTPSecretByUser == nil {
		s.TOTPSecretByUser = map[string]string{}
	}
	s.TOTPSecretByUser[auth.ProjectID+":"+auth.UserID] = secret
	if s.TOTPStore != nil {
		if err := s.TOTPStore.PutTOTP(ctx, auth, secret); err != nil {
			_ = s.audit(ctx, auth, ActionTOTP, "totp", auth.UserID, "failed", nil)
			return "", "", err
		}
	}
	_ = s.audit(ctx, auth, ActionTOTP, "totp", auth.UserID, "success", nil)
	return secret, TOTPURI("Opsi", auth.ProjectID+":"+auth.UserID, secret), nil
}

func (s *Service) Create(ctx context.Context, auth AuthContext, ref SecretRef) (SecretValue, error) {
	verified, err := s.authorize(ctx, auth)
	if err != nil {
		_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "denied", map[string]string{"reason": "auth"})
		return SecretValue{}, err
	}
	auth = verified
	if !CanCreate(auth.Role) {
		_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "denied", map[string]string{"reason": "rbac"})
		return SecretValue{}, errors.New("permission denied")
	}
	if err := s.verifyEncryption(ctx); err != nil {
		_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "failed", map[string]string{"reason": "encryption"})
		return SecretValue{}, err
	}
	value, err := GenerateCredentials()
	if err != nil {
		_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "failed", map[string]string{"reason": "entropy"})
		return SecretValue{}, err
	}
	if err := s.store().Put(ctx, ref, value); err != nil {
		_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "failed", nil)
		return SecretValue{}, err
	}
	_ = s.audit(ctx, auth, ActionCreate, "secret", ref.Name, "success", nil)
	return value, nil
}

func (s *Service) Reveal(ctx context.Context, auth AuthContext, ref SecretRef, otpRequestID, otpCode, totpCode string) (SecretValue, error) {
	verified, err := s.authorize(ctx, auth)
	if err != nil {
		_ = s.audit(ctx, auth, ActionReveal, "secret", ref.Name, "denied", map[string]string{"reason": "auth"})
		return SecretValue{}, err
	}
	auth = verified
	if !CanReveal(auth.Role) {
		_ = s.audit(ctx, auth, ActionReveal, "secret", ref.Name, "denied", map[string]string{"reason": "rbac"})
		return SecretValue{}, errors.New("permission denied")
	}
	if err := s.verifySecondFactor(ctx, auth, ref, otpRequestID, otpCode, totpCode); err != nil {
		_ = s.audit(ctx, auth, ActionReveal, "secret", ref.Name, "denied", map[string]string{"reason": "second_factor"})
		return SecretValue{}, err
	}
	value, err := s.store().Get(ctx, ref)
	if err != nil {
		_ = s.audit(ctx, auth, ActionReveal, "secret", ref.Name, "failed", nil)
		return SecretValue{}, err
	}
	_ = s.audit(ctx, auth, ActionReveal, "secret", ref.Name, "success", nil)
	return value, nil
}

func (s *Service) Rotate(ctx context.Context, auth AuthContext, ref SecretRef, otpRequestID, otpCode, totpCode string) (SecretValue, error) {
	verified, err := s.authorize(ctx, auth)
	if err != nil {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "denied", map[string]string{"reason": "auth"})
		return SecretValue{}, err
	}
	auth = verified
	if !CanRotate(auth.Role) {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "denied", map[string]string{"reason": "rbac"})
		return SecretValue{}, errors.New("permission denied")
	}
	if err := s.verifyEncryption(ctx); err != nil {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "failed", map[string]string{"reason": "encryption"})
		return SecretValue{}, err
	}
	if err := s.verifySecondFactor(ctx, auth, ref, otpRequestID, otpCode, totpCode); err != nil {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "denied", map[string]string{"reason": "second_factor"})
		return SecretValue{}, err
	}
	value, err := GenerateCredentials()
	if err != nil {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "failed", map[string]string{"reason": "entropy"})
		return SecretValue{}, err
	}
	if err := s.store().Put(ctx, ref, value); err != nil {
		_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "failed", nil)
		return SecretValue{}, err
	}
	if s.Restarter != nil {
		if err := s.Restarter.Restart(ctx, ref); err != nil {
			_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "failed", map[string]string{"reason": "rollout_restart"})
			return SecretValue{}, err
		}
	}
	_ = s.audit(ctx, auth, ActionRotate, "secret", ref.Name, "success", nil)
	return value, nil
}

func (s *Service) verifySecondFactor(ctx context.Context, auth AuthContext, ref SecretRef, otpRequestID, otpCode, totpCode string) error {
	if s.OTP != nil && otpCode != "" && otpRequestID != "" {
		otpCtx := ctx
		cancel := func() {}
		if s.CloudOTPTimeout > 0 {
			otpCtx, cancel = context.WithTimeout(ctx, s.CloudOTPTimeout)
		}
		defer cancel()
		if err := s.OTP.VerifyOTP(otpCtx, auth, otpRequestID, "secret.reveal", otpCode); err == nil {
			return nil
		}
	}
	secret := s.TOTPSecretByUser[auth.ProjectID+":"+auth.UserID]
	if secret == "" && s.TOTPStore != nil {
		if stored, err := s.TOTPStore.GetTOTP(ctx, auth); err == nil {
			secret = stored
			if s.TOTPSecretByUser == nil {
				s.TOTPSecretByUser = map[string]string{}
			}
			s.TOTPSecretByUser[auth.ProjectID+":"+auth.UserID] = stored
		}
	}
	if VerifyTOTP(secret, totpCode, s.now(), 1) {
		return nil
	}
	return errors.New("second factor verification failed")
}

func (s *Service) authorize(ctx context.Context, auth AuthContext) (AuthContext, error) {
	if s.Auth == nil {
		return auth, nil
	}
	return s.Auth.VerifyAuth(ctx, auth)
}

func (s *Service) verifyEncryption(ctx context.Context) error {
	if s.Encryption == nil {
		return errors.New("k3s encryption verifier is not configured")
	}
	return s.Encryption.Verify(ctx)
}

func (s *Service) store() Store {
	if s.Store == nil {
		s.Store = NewMemoryStore()
	}
	return s.Store
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) audit(ctx context.Context, auth AuthContext, action, resourceType, resourceID, result string, metadata map[string]string) error {
	if s.Audit == nil {
		return nil
	}
	metadataJSON := "{}"
	if len(metadata) > 0 {
		data, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		metadataJSON = string(data)
	}
	return s.Audit.InsertAudit(ctx, AuditRecord{
		ID:           newAuditID(),
		ProjectID:    auth.ProjectID,
		Actor:        auth.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Result:       result,
		MetadataJSON: metadataJSON,
		CreatedAt:    s.now(),
	})
}

func newAuditID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("audit-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
