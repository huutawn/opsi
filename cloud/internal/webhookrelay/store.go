package webhookrelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type Envelope struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	ServiceID         string     `json:"service_id"`
	ServiceName       string     `json:"service_name"`
	ServiceType       string     `json:"service_type"`
	RepoURL           string     `json:"repo_url"`
	Ref               string     `json:"ref"`
	After             string     `json:"after"`
	Branch            string     `json:"branch"`
	TriggeredBy       string     `json:"triggered_by"`
	Body              string     `json:"body,omitempty"`
	Signature         string     `json:"signature"`
	Modified          []string   `json:"modified,omitempty"`
	Status            string     `json:"status,omitempty"`
	AttemptCount      int        `json:"attempt_count,omitempty"`
	NextRetryAt       *time.Time `json:"next_retry_at,omitempty"`
	LastErrorRedacted string     `json:"last_error_redacted,omitempty"`
	IdempotencyKey    string     `json:"idempotency_key,omitempty"`
	ReceivedAt        time.Time  `json:"received_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	UpdatedAt         time.Time  `json:"updated_at,omitempty"`
}

type RelayQueue interface {
	Enqueue(Envelope) error
	Next(context.Context, string, time.Duration) (*Envelope, error)
	PurgeExpired(time.Time)
	Len() int
}

type Queue struct {
	mu          sync.Mutex
	changed     chan struct{}
	now         func() time.Time
	items       []Envelope
	idempotency map[string]string
}

func NewQueue() *Queue {
	return &Queue{changed: make(chan struct{}), now: time.Now, idempotency: map[string]string{}}
}

func (q *Queue) Enqueue(env Envelope) error {
	if env.ProjectID == "" || env.ServiceID == "" {
		return errors.New("project_id and service_id are required")
	}
	env, bodyHash, err := sanitizeEnvelope(env)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if prior := q.idempotency[env.IdempotencyKey]; prior != "" {
		if prior != bodyHash {
			return errors.New("idempotency key reused with different relay body")
		}
		return nil
	}
	q.idempotency[env.IdempotencyKey] = bodyHash
	q.items = append(q.items, env)
	q.signalLocked()
	return nil
}

func (q *Queue) Next(ctx context.Context, projectID string, wait time.Duration) (*Envelope, error) {
	deadline := q.now().Add(wait)
	for {
		q.mu.Lock()
		q.purgeLocked(q.now())
		for i, item := range q.items {
			if projectID != "" && item.ProjectID != projectID {
				continue
			}
			q.items = append(q.items[:i], q.items[i+1:]...)
			item.Status = "delivered"
			item.AttemptCount++
			item.UpdatedAt = q.now().UTC()
			q.mu.Unlock()
			return &item, nil
		}
		changed := q.changed
		q.mu.Unlock()

		remaining := time.Until(deadline)
		if wait <= 0 || remaining <= 0 {
			return nil, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			return nil, nil
		}
	}
}

func (q *Queue) PurgeExpired(now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.purgeLocked(now)
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.purgeLocked(q.now())
	return len(q.items)
}

func (q *Queue) purgeLocked(now time.Time) {
	kept := q.items[:0]
	for _, item := range q.items {
		if item.ExpiresAt.After(now) {
			kept = append(kept, item)
		}
	}
	q.items = kept
}

func (q *Queue) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

type PostgresQueue struct {
	DB  *sql.DB
	now func() time.Time
}

func NewPostgresQueue(db *sql.DB) *PostgresQueue {
	return &PostgresQueue{DB: db, now: time.Now}
}

func (q *PostgresQueue) Enqueue(env Envelope) error {
	if q == nil || q.DB == nil {
		return errors.New("postgres relay queue requires database")
	}
	if env.ProjectID == "" || env.ServiceID == "" {
		return errors.New("project_id and service_id are required")
	}
	env, bodyHash, err := sanitizeEnvelope(env)
	if err != nil {
		return err
	}
	now := q.clock()
	if env.ReceivedAt.IsZero() {
		env.ReceivedAt = now
	}
	if env.ExpiresAt.IsZero() {
		env.ExpiresAt = now.Add(24 * time.Hour)
	}
	meta, err := json.Marshal(env)
	if err != nil {
		return err
	}
	tx, err := q.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var orgID string
	if err := tx.QueryRowContext(context.Background(), `SELECT org_id FROM projects WHERE id=$1`, env.ProjectID).Scan(&orgID); err != nil {
		return fmt.Errorf("relay project lookup: %w", err)
	}
	res, err := tx.ExecContext(context.Background(), `INSERT INTO relay_jobs (id, org_id, project_id, target_service_id, target_service_name, target_service_type, type, status, body_hash, redacted_body, idempotency_key, attempt_count, max_attempts, created_by, created_at, updated_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,'github_push','queued',$7,$8,$9,0,3,$10,$11,$11,$12)
		ON CONFLICT (project_id, idempotency_key) DO NOTHING`,
		env.ID, orgID, env.ProjectID, env.ServiceID, env.ServiceName, env.ServiceType, bodyHash, meta, env.IdempotencyKey, env.TriggeredBy, env.ReceivedAt, env.ExpiresAt)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		var existingHash string
		if err := tx.QueryRowContext(context.Background(), `SELECT body_hash FROM relay_jobs WHERE project_id=$1 AND idempotency_key=$2`, env.ProjectID, env.IdempotencyKey).Scan(&existingHash); err != nil {
			return err
		}
		if existingHash != bodyHash {
			return errors.New("idempotency key reused with different relay body")
		}
		return tx.Commit()
	}
	if err := insertRelayEvent(tx, orgID, env.ProjectID, env.ID, "created", "relay job created", map[string]any{"status": "queued"}); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *PostgresQueue) Next(ctx context.Context, projectID string, wait time.Duration) (*Envelope, error) {
	deadline := q.clock().Add(wait)
	for {
		env, err := q.claim(ctx, projectID)
		if err != nil || env != nil || wait <= 0 || !time.Now().Before(deadline) {
			return env, err
		}
		timer := time.NewTimer(minDuration(100*time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (q *PostgresQueue) PurgeExpired(now time.Time) {
	if q == nil || q.DB == nil {
		return
	}
	rows, err := q.DB.QueryContext(context.Background(), `UPDATE relay_jobs SET status='expired', updated_at=$1, last_error_redacted='relay job expired'
		WHERE status IN ('queued','claimed') AND expires_at <= $1 RETURNING org_id, project_id, id`, now.UTC())
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var orgID, projectID, id string
		if rows.Scan(&orgID, &projectID, &id) == nil {
			_ = insertRelayEvent(q.DB, orgID, projectID, id, "expired", "relay job expired", map[string]any{"status": "expired"})
		}
	}
}

func (q *PostgresQueue) Len() int {
	if q == nil || q.DB == nil {
		return 0
	}
	var count int
	_ = q.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM relay_jobs WHERE status='queued' AND expires_at > now()`).Scan(&count)
	return count
}

func (q *PostgresQueue) claim(ctx context.Context, projectID string) (*Envelope, error) {
	if q == nil || q.DB == nil {
		return nil, errors.New("postgres relay queue requires database")
	}
	tx, err := q.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	whereProject := ""
	args := []any{q.clock()}
	if projectID != "" {
		whereProject = " AND project_id=$2"
		args = append(args, projectID)
	}
	row := tx.QueryRowContext(ctx, `SELECT id, org_id, project_id, redacted_body, attempt_count, next_retry_at FROM relay_jobs
		WHERE status='queued' AND expires_at > $1 AND (next_retry_at IS NULL OR next_retry_at <= $1)`+whereProject+`
		ORDER BY created_at ASC FOR UPDATE SKIP LOCKED LIMIT 1`, args...)
	var id, orgID, proj string
	var data []byte
	var attempts int
	var nextRetry sql.NullTime
	if err := row.Scan(&id, &orgID, &proj, &data, &attempts, &nextRetry); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	env.Status = "delivered"
	env.AttemptCount = attempts + 1
	if nextRetry.Valid {
		env.NextRetryAt = &nextRetry.Time
	}
	env.UpdatedAt = q.clock()
	updated, _ := json.Marshal(env)
	if _, err := tx.ExecContext(ctx, `UPDATE relay_jobs SET status='delivered', redacted_body=$1, attempt_count=attempt_count+1, updated_at=$2 WHERE id=$3`, updated, env.UpdatedAt, id); err != nil {
		return nil, err
	}
	if err := insertRelayEvent(tx, orgID, proj, id, "claimed", "relay job claimed", map[string]any{"attempt_count": env.AttemptCount}); err != nil {
		return nil, err
	}
	if err := insertRelayEvent(tx, orgID, proj, id, "delivered", "relay job delivered to agent", map[string]any{"status": "delivered"}); err != nil {
		return nil, err
	}
	return &env, tx.Commit()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertRelayEvent(db execer, orgID, projectID, jobID, eventType, message string, metadata map[string]any) error {
	meta, _ := json.Marshal(registry.RedactMap(metadata))
	_, err := db.ExecContext(context.Background(), `INSERT INTO relay_events (id, org_id, project_id, job_id, type, message_redacted, metadata_redacted, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())`, newID(), orgID, projectID, jobID, eventType, registry.RedactString(message), meta)
	return err
}

func sanitizeEnvelope(env Envelope) (Envelope, string, error) {
	if env.ID == "" {
		env.ID = newID()
	}
	bodyHash := sha256Hex([]byte(env.Body))
	if env.IdempotencyKey == "" {
		env.IdempotencyKey = bodyHash
	}
	if len(env.Modified) == 0 && env.Body != "" {
		env.Modified = changedFilesFromGitHubBody([]byte(env.Body))
	}
	env.ServiceName = registry.RedactString(env.ServiceName)
	env.ServiceType = registry.RedactString(env.ServiceType)
	env.RepoURL = registry.RedactString(env.RepoURL)
	env.Ref = registry.RedactString(env.Ref)
	env.After = registry.RedactString(env.After)
	env.Branch = registry.RedactString(env.Branch)
	env.TriggeredBy = registry.RedactString(env.TriggeredBy)
	env.Signature = registry.RedactString(env.Signature)
	for i := range env.Modified {
		env.Modified[i] = registry.RedactString(env.Modified[i])
	}
	env.Body = ""
	env.Status = firstNonEmpty(env.Status, "queued")
	return env, bodyHash, nil
}

func changedFilesFromGitHubBody(body []byte) []string {
	var payload struct {
		Commits []struct {
			Modified []string `json:"modified"`
			Added    []string `json:"added"`
			Removed  []string `json:"removed"`
		} `json:"commits"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, commit := range payload.Commits {
		for _, group := range [][]string{commit.Modified, commit.Added, commit.Removed} {
			for _, file := range group {
				if file == "" || seen[file] {
					continue
				}
				seen[file] = true
				out = append(out, file)
			}
		}
	}
	return out
}

func (q *PostgresQueue) clock() time.Time {
	if q.now != nil {
		return q.now().UTC()
	}
	return time.Now().UTC()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
