package webhookrelay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
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
	EnsureBinding(context.Context, resourcev1.BindingCredentialSpec) (resourcev1.ManagedResourceCredential, error)
	EnsureWorkloadSecret(context.Context, resourcev1.WorkloadSecretSpec) (resourcev1.ManagedResourceCredential, error)
	ListWorkloadSecrets(context.Context, string, string) ([]resourcev1.WorkloadSecretMetadata, error)
	GetWorkloadSecret(context.Context, string, string, string) (resourcev1.WorkloadSecretMetadata, error)
	UpsertWorkloadSecret(context.Context, resourcev1.WorkloadSecretUpsert) (resourcev1.WorkloadSecretMetadata, bool, error)
	BindWorkloadSecret(context.Context, string, string, string, string) (resourcev1.WorkloadSecretMetadata, error)
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
	resourceID := strings.TrimPrefix(id, "mrcred-")
	credential, err := s.Get(ctx, id)
	if err == nil {
		if (credential.Purpose != "" && credential.Purpose != resourcev1.CredentialPurposeResourceManagement) || (credential.OwnerID != "" && credential.OwnerID != resourceID) || (credential.ResourceID != "" && credential.ResourceID != resourceID) {
			return resourcev1.ManagedResourceCredential{}, errors.New("management credential identity conflict")
		}
		if credential.Purpose == "" || credential.OwnerID == "" || credential.ResourceID == "" {
			credential.Purpose, credential.OwnerID, credential.ResourceID = resourcev1.CredentialPurposeResourceManagement, resourceID, resourceID
			return s.update(ctx, credential)
		}
		return credential, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.ManagedResourceCredential{}, err
	}
	password := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential = resourcev1.ManagedResourceCredential{CredentialID: id, Purpose: resourcev1.CredentialPurposeResourceManagement, OwnerID: resourceID, ResourceID: resourceID, Username: "opsi", Password: base64.RawURLEncoding.EncodeToString(password), Database: "opsi"}
	stored, err := s.insert(ctx, credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	if stored.Purpose != resourcev1.CredentialPurposeResourceManagement || stored.OwnerID != resourceID || stored.ResourceID != resourceID {
		return resourcev1.ManagedResourceCredential{}, errors.New("management credential identity conflict")
	}
	return stored, nil
}

func (s postgresManagedResourceCredentialVault) EnsureBinding(ctx context.Context, spec resourcev1.BindingCredentialSpec) (resourcev1.ManagedResourceCredential, error) {
	credential, err := s.Get(ctx, spec.CredentialID)
	if err == nil {
		if credential.ValidateBinding(spec.BindingID, spec.ResourceID) != nil || credential.Username != spec.Username || credential.Database != spec.Database {
			return resourcev1.ManagedResourceCredential{}, errors.New("binding credential identity conflict")
		}
		return credential, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.ManagedResourceCredential{}, err
	}
	password := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential = resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeResourceBinding, OwnerID: spec.BindingID, ResourceID: spec.ResourceID, Username: spec.Username, Password: base64.RawURLEncoding.EncodeToString(password), Database: spec.Database}
	stored, err := s.insert(ctx, credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	if stored.ValidateBinding(spec.BindingID, spec.ResourceID) != nil || stored.Username != spec.Username || stored.Database != spec.Database {
		return resourcev1.ManagedResourceCredential{}, errors.New("binding credential identity conflict")
	}
	return stored, nil
}

func (s postgresManagedResourceCredentialVault) EnsureWorkloadSecret(ctx context.Context, spec resourcev1.WorkloadSecretSpec) (resourcev1.ManagedResourceCredential, error) {
	credential, err := s.Get(ctx, spec.CredentialID)
	if err == nil {
		if credential.ValidateWorkloadSecret(spec.ProjectID, spec.ServiceID) != nil {
			return resourcev1.ManagedResourceCredential{}, errors.New("workload secret identity conflict")
		}
		if err := s.ensureWorkloadSecretMetadata(ctx, spec); err != nil {
			return resourcev1.ManagedResourceCredential{}, err
		}
		return credential, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.ManagedResourceCredential{}, err
	}
	value := make([]byte, 48)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential = resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeWorkloadSecret, OwnerID: spec.ServiceID, ResourceID: spec.ProjectID, Username: "value", Password: base64.RawURLEncoding.EncodeToString(value)}
	stored, err := s.insert(ctx, credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	if stored.ValidateWorkloadSecret(spec.ProjectID, spec.ServiceID) != nil {
		return resourcev1.ManagedResourceCredential{}, errors.New("workload secret identity conflict")
	}
	if err := s.ensureWorkloadSecretMetadata(ctx, spec); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	return stored, nil
}

func (s postgresManagedResourceCredentialVault) ensureWorkloadSecretMetadata(ctx context.Context, spec resourcev1.WorkloadSecretSpec) error {
	if spec.LogicalName == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workload_secret_metadata(id,project_id,service_id,logical_name,revision,status,updated_at) VALUES($1,$2,$3,$4,1,'ready',NOW()) ON CONFLICT(project_id,service_id,logical_name) DO NOTHING`, spec.CredentialID, spec.ProjectID, spec.ServiceID, spec.LogicalName)
	return err
}

func (s postgresManagedResourceCredentialVault) ListWorkloadSecrets(ctx context.Context, projectID, serviceID string) ([]resourcev1.WorkloadSecretMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,service_id,logical_name,revision,status,updated_at FROM workload_secret_metadata WHERE project_id=$1 AND service_id=$2 ORDER BY logical_name`, projectID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resourcev1.WorkloadSecretMetadata{}
	for rows.Next() {
		value, scanErr := scanWorkloadSecretMetadata(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s postgresManagedResourceCredentialVault) GetWorkloadSecret(ctx context.Context, projectID, serviceID, logicalName string) (resourcev1.WorkloadSecretMetadata, error) {
	return scanWorkloadSecretMetadata(s.db.QueryRowContext(ctx, `SELECT id,project_id,service_id,logical_name,revision,status,updated_at FROM workload_secret_metadata WHERE project_id=$1 AND service_id=$2 AND logical_name=$3`, projectID, serviceID, logicalName))
}

func (s postgresManagedResourceCredentialVault) UpsertWorkloadSecret(ctx context.Context, spec resourcev1.WorkloadSecretUpsert) (resourcev1.WorkloadSecretMetadata, bool, error) {
	if spec.CredentialID == "" || spec.ProjectID == "" || spec.ServiceID == "" || resourcev1.ValidateWorkloadSecretLogicalName(spec.LogicalName) != nil || spec.IdempotencyKey == "" || spec.Value == "" || len(spec.Value) > 8192 || strings.ContainsAny(spec.Value, "\x00\r\n") {
		return resourcev1.WorkloadSecretMetadata{}, false, errors.New("workload secret upsert is invalid")
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeWorkloadSecret, OwnerID: spec.ServiceID, ResourceID: spec.ProjectID, Username: "value", Password: spec.Value}
	if err := credential.ValidateWorkloadSecret(spec.ProjectID, spec.ServiceID); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	payload := sha256.Sum256([]byte(spec.ProjectID + "\x00" + spec.ServiceID + "\x00" + spec.LogicalName + "\x00" + spec.Value))
	payloadHash := hex.EncodeToString(payload[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	defer tx.Rollback()
	var replayHash, replayID string
	var replayRevision uint64
	replayErr := tx.QueryRowContext(ctx, `SELECT payload_hash,secret_id,revision FROM workload_secret_upserts WHERE project_id=$1 AND idempotency_key=$2`, spec.ProjectID, spec.IdempotencyKey).Scan(&replayHash, &replayID, &replayRevision)
	if replayErr == nil {
		if replayHash != payloadHash || replayID != spec.CredentialID {
			return resourcev1.WorkloadSecretMetadata{}, false, errors.New("workload secret idempotency conflict")
		}
		metadata, getErr := scanWorkloadSecretMetadata(tx.QueryRowContext(ctx, `SELECT id,project_id,service_id,logical_name,revision,status,updated_at FROM workload_secret_metadata WHERE id=$1`, replayID))
		if getErr != nil {
			return resourcev1.WorkloadSecretMetadata{}, false, getErr
		}
		if err := tx.Commit(); err != nil {
			return resourcev1.WorkloadSecretMetadata{}, false, err
		}
		return metadata, true, nil
	}
	if !errors.Is(replayErr, sql.ErrNoRows) {
		return resourcev1.WorkloadSecretMetadata{}, false, replayErr
	}
	var revision uint64
	currentErr := tx.QueryRowContext(ctx, `SELECT revision FROM workload_secret_metadata WHERE project_id=$1 AND service_id=$2 AND logical_name=$3 FOR UPDATE`, spec.ProjectID, spec.ServiceID, spec.LogicalName).Scan(&revision)
	if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
		return resourcev1.WorkloadSecretMetadata{}, false, currentErr
	}
	revision++
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_resource_credentials(id,ciphertext,nonce,updated_at) VALUES($1,$2,$3,NOW()) ON CONFLICT(id) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,nonce=EXCLUDED.nonce,updated_at=NOW()`, spec.CredentialID, ciphertext, nonce); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workload_secret_metadata(id,project_id,service_id,logical_name,revision,status,updated_at) VALUES($1,$2,$3,$4,$5,'ready',NOW()) ON CONFLICT(project_id,service_id,logical_name) DO UPDATE SET id=EXCLUDED.id,revision=EXCLUDED.revision,status='ready',updated_at=NOW()`, spec.CredentialID, spec.ProjectID, spec.ServiceID, spec.LogicalName, revision); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workload_secret_upserts(project_id,idempotency_key,payload_hash,secret_id,revision,created_at) VALUES($1,$2,$3,$4,$5,NOW())`, spec.ProjectID, spec.IdempotencyKey, payloadHash, spec.CredentialID, revision); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	metadata, err := scanWorkloadSecretMetadata(tx.QueryRowContext(ctx, `SELECT id,project_id,service_id,logical_name,revision,status,updated_at FROM workload_secret_metadata WHERE id=$1`, spec.CredentialID))
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	return metadata, false, nil
}

func (s postgresManagedResourceCredentialVault) BindWorkloadSecret(ctx context.Context, projectID, currentScope, serviceID, logicalName string) (resourcev1.WorkloadSecretMetadata, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	defer tx.Rollback()
	metadata, err := scanWorkloadSecretMetadata(tx.QueryRowContext(ctx, `SELECT id,project_id,service_id,logical_name,revision,status,updated_at FROM workload_secret_metadata WHERE project_id=$1 AND service_id=$2 AND logical_name=$3 FOR UPDATE`, projectID, currentScope, logicalName))
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	var ciphertext, nonce []byte
	if err := tx.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM managed_resource_credentials WHERE id=$1 FOR UPDATE`, metadata.ID).Scan(&ciphertext, &nonce); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	var credential resourcev1.ManagedResourceCredential
	if err := s.open(ciphertext, nonce, &credential); err != nil || credential.ValidateWorkloadSecret(projectID, currentScope) != nil {
		return resourcev1.WorkloadSecretMetadata{}, errors.New("workload secret credential is invalid")
	}
	credential.OwnerID = serviceID
	ciphertext, nonce, err = s.seal(credential)
	if err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE managed_resource_credentials SET ciphertext=$2,nonce=$3,updated_at=NOW() WHERE id=$1`, metadata.ID, ciphertext, nonce); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workload_secret_metadata SET service_id=$2,updated_at=NOW() WHERE id=$1`, metadata.ID, serviceID); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	metadata.ServiceID = serviceID
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM workload_secret_metadata WHERE id=$1`, metadata.ID).Scan(&metadata.UpdatedAt); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, err
	}
	return metadata, tx.Commit()
}

type workloadSecretScanner interface{ Scan(...any) error }

func scanWorkloadSecretMetadata(row workloadSecretScanner) (resourcev1.WorkloadSecretMetadata, error) {
	var value resourcev1.WorkloadSecretMetadata
	err := row.Scan(&value.ID, &value.ProjectID, &value.ServiceID, &value.LogicalName, &value.Revision, &value.Status, &value.UpdatedAt)
	value.Reference = "workload-secret://" + value.ID
	return value, err
}

func (s postgresManagedResourceCredentialVault) insert(ctx context.Context, credential resourcev1.ManagedResourceCredential) (resourcev1.ManagedResourceCredential, error) {
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_resource_credentials(id,ciphertext,nonce,updated_at) VALUES($1,$2,$3,now()) ON CONFLICT(id) DO NOTHING`, credential.CredentialID, ciphertext, nonce)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	return s.Get(ctx, credential.CredentialID)
}

func (s postgresManagedResourceCredentialVault) update(ctx context.Context, credential resourcev1.ManagedResourceCredential) (resourcev1.ManagedResourceCredential, error) {
	ciphertext, nonce, err := s.seal(credential)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_resource_credentials SET ciphertext=$1,nonce=$2,updated_at=now() WHERE id=$3`, ciphertext, nonce, credential.CredentialID)
	if err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return resourcev1.ManagedResourceCredential{}, sql.ErrNoRows
	}
	return credential, nil
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
