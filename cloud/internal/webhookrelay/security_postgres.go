package webhookrelay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type encryptedPostgresStore struct {
	db  *sql.DB
	aes cipher.AEAD
}

func NewPostgresCredentialVault(db *sql.DB, key string) (CredentialVault, error) {
	store, err := newEncryptedPostgresStore(db, key)
	if err != nil {
		return nil, err
	}
	return postgresCredentialVault{encryptedPostgresStore: store}, nil
}

func NewPostgresRegistrationVault(db *sql.DB, key string) (RegistrationVault, error) {
	store, err := newEncryptedPostgresStore(db, key)
	if err != nil {
		return nil, err
	}
	return postgresRegistrationVault{encryptedPostgresStore: store}, nil
}

func NewPostgresRegistryPullCredentialVault(db *sql.DB, key string) (RegistryPullCredentialVault, error) {
	store, err := newEncryptedPostgresStore(db, key)
	if err != nil {
		return nil, err
	}
	return postgresRegistryPullCredentialVault{encryptedPostgresStore: store}, nil
}

type ManagedResourceCredentialVault interface {
	Ensure(context.Context, string) (resourcev1.ManagedResourceCredential, error)
	Get(context.Context, string) (resourcev1.ManagedResourceCredential, error)
	Delete(context.Context, string) error
}

type postgresManagedResourceCredentialVault struct{ encryptedPostgresStore }

func NewPostgresManagedResourceCredentialVault(db *sql.DB, key string) (ManagedResourceCredentialVault, error) {
	store, err := newEncryptedPostgresStore(db, key)
	if err != nil {
		return nil, err
	}
	return postgresManagedResourceCredentialVault{encryptedPostgresStore: store}, nil
}

func (s postgresManagedResourceCredentialVault) Ensure(ctx context.Context, id string) (resourcev1.ManagedResourceCredential, error) {
	credential, err := s.Get(ctx, id)
	if err == nil {
		return credential, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.ManagedResourceCredential{}, err
	}
	password := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential = resourcev1.ManagedResourceCredential{CredentialID: id, Username: "opsi", Password: base64.RawURLEncoding.EncodeToString(password), Database: "opsi"}
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_resource_credentials(id,ciphertext,nonce,updated_at) VALUES($1,$2,$3,now()) ON CONFLICT(id) DO NOTHING`, id, ciphertext, nonce)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	return s.Get(ctx, id)
}

func (s postgresManagedResourceCredentialVault) Get(ctx context.Context, id string) (resourcev1.ManagedResourceCredential, error) {
	var ciphertext, nonce []byte
	if err := s.db.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM managed_resource_credentials WHERE id=$1`, id).Scan(&ciphertext, &nonce); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	var credential resourcev1.ManagedResourceCredential
	if err := s.open(ciphertext, nonce, &credential); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	return credential, credential.Validate()
}

func (s postgresManagedResourceCredentialVault) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_resource_credentials WHERE id=$1`, id)
	return err
}

func newEncryptedPostgresStore(db *sql.DB, key string) (encryptedPostgresStore, error) {
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return encryptedPostgresStore{}, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedPostgresStore{}, err
	}
	return encryptedPostgresStore{db: db, aes: aesgcm}, nil
}

func (s encryptedPostgresStore) seal(v any) ([]byte, []byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, s.aes.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return s.aes.Seal(nil, nonce, plain, nil), nonce, nil
}

func (s encryptedPostgresStore) open(ciphertext, nonce []byte, dst any) error {
	plain, err := s.aes.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, dst)
}

type postgresCredentialVault struct {
	encryptedPostgresStore
}

func (s postgresCredentialVault) Put(sessionID string, credential BootstrapCredential, ttl time.Duration) {
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO bootstrap_credentials(session_id, ciphertext, nonce, expires_at)
		VALUES($1,$2,$3,$4)
		ON CONFLICT(session_id) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, expires_at = EXCLUDED.expires_at, consumed_at = NULL`,
		sessionID, ciphertext, nonce, time.Now().UTC().Add(ttl))
}

func (s postgresCredentialVault) GetForBootstrapLease(sessionID string) (BootstrapCredential, bool) {
	ctx := context.Background()
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM bootstrap_credentials
		WHERE session_id = $1 AND expires_at > now()`, sessionID).Scan(&ciphertext, &nonce)
	if err != nil {
		return BootstrapCredential{}, false
	}
	var credential BootstrapCredential
	if err := s.open(ciphertext, nonce, &credential); err != nil {
		return BootstrapCredential{}, false
	}
	return credential, true
}

func (s postgresCredentialVault) Delete(sessionID string) {
	_, _ = s.db.ExecContext(context.Background(), `DELETE FROM bootstrap_credentials WHERE session_id = $1`, sessionID)
}

func (s postgresCredentialVault) Len() int {
	var n int
	_ = s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM bootstrap_credentials WHERE consumed_at IS NULL AND expires_at > now()`).Scan(&n)
	return n
}

type postgresRegistrationVault struct {
	encryptedPostgresStore
}

type postgresRegistryPullCredentialVault struct {
	encryptedPostgresStore
}

func (s postgresRegistryPullCredentialVault) Put(ctx context.Context, id string, credential deploymentv1.RegistryPullCredential) error {
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO registry_pull_credentials(id, ciphertext, nonce, updated_at)
		VALUES($1,$2,$3,now())
		ON CONFLICT(id) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`, id, ciphertext, nonce)
	return err
}

func (s postgresRegistryPullCredentialVault) Get(ctx context.Context, id string) (deploymentv1.RegistryPullCredential, bool, error) {
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM registry_pull_credentials WHERE id = $1`, id).Scan(&ciphertext, &nonce)
	if err == sql.ErrNoRows {
		return deploymentv1.RegistryPullCredential{}, false, nil
	}
	if err != nil {
		return deploymentv1.RegistryPullCredential{}, false, err
	}
	var credential deploymentv1.RegistryPullCredential
	if err := s.open(ciphertext, nonce, &credential); err != nil {
		return deploymentv1.RegistryPullCredential{}, false, err
	}
	return credential, true, nil
}

func (s postgresRegistrationVault) Put(sessionID, orgID, projectID, nodeID, token string, ttl time.Duration) {
	ciphertext, nonce, err := s.seal(token)
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO bootstrap_registration_tokens(session_id, org_id, project_id, node_id, token_hash, token_ciphertext, token_nonce, expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(session_id) DO UPDATE SET token_hash = EXCLUDED.token_hash, token_ciphertext = EXCLUDED.token_ciphertext, token_nonce = EXCLUDED.token_nonce, expires_at = EXCLUDED.expires_at, worker_consumed_at = NULL, exchanged_at = NULL`,
		sessionID, orgID, projectID, nodeID, tokenHash(token), ciphertext, nonce, time.Now().UTC().Add(ttl))
}

func (s postgresRegistrationVault) GetForBootstrapLease(sessionID string) (BootstrapRegistration, bool) {
	var reg BootstrapRegistration
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(context.Background(), `SELECT session_id, org_id, project_id, node_id, token_ciphertext, token_nonce, expires_at
		FROM bootstrap_registration_tokens WHERE session_id = $1 AND expires_at > now()`, sessionID).Scan(&reg.SessionID, &reg.OrgID, &reg.ProjectID, &reg.NodeID, &ciphertext, &nonce, &reg.ExpiresAt)
	if err != nil {
		return BootstrapRegistration{}, false
	}
	if err := s.open(ciphertext, nonce, &reg.Token); err != nil {
		return BootstrapRegistration{}, false
	}
	return reg, true
}

func (s postgresRegistrationVault) Exchange(token string) (BootstrapRegistration, bool) {
	var reg BootstrapRegistration
	err := s.db.QueryRowContext(context.Background(), `UPDATE bootstrap_registration_tokens SET exchanged_at = now()
		WHERE token_hash = $1 AND exchanged_at IS NULL AND expires_at > now()
		RETURNING session_id, org_id, project_id, node_id, expires_at`, tokenHash(token)).Scan(&reg.SessionID, &reg.OrgID, &reg.ProjectID, &reg.NodeID, &reg.ExpiresAt)
	if err != nil {
		return BootstrapRegistration{}, false
	}
	return reg, true
}

func (s postgresRegistrationVault) DeleteSession(sessionID string) {
	_, _ = s.db.ExecContext(context.Background(), `DELETE FROM bootstrap_registration_tokens WHERE session_id = $1`, sessionID)
}

type postgresRateLimiter struct {
	db *sql.DB
}

func NewPostgresRateLimiter(db *sql.DB) RateLimiter {
	return postgresRateLimiter{db: db}
}

func (l postgresRateLimiter) Allow(key string, limit int, window time.Duration) bool {
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var count int
	var resets time.Time
	err = tx.QueryRowContext(ctx, `SELECT count, resets_at FROM rate_limits WHERE key = $1 FOR UPDATE`, key).Scan(&count, &resets)
	if err == sql.ErrNoRows || !now.Before(resets) {
		_, err = tx.ExecContext(ctx, `INSERT INTO rate_limits(key, count, resets_at, updated_at) VALUES($1,1,$2,$3)
			ON CONFLICT(key) DO UPDATE SET count = 1, resets_at = EXCLUDED.resets_at, updated_at = EXCLUDED.updated_at`, key, now.Add(window), now)
		return err == nil && tx.Commit() == nil
	}
	if err != nil || count >= limit {
		return false
	}
	_, err = tx.ExecContext(ctx, `UPDATE rate_limits SET count = count + 1, updated_at = $2 WHERE key = $1`, key, now)
	return err == nil && tx.Commit() == nil
}
