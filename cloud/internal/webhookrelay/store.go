package webhookrelay

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Envelope struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ServiceID   string    `json:"service_id"`
	ServiceName string    `json:"service_name"`
	ServiceType string    `json:"service_type"`
	RepoURL     string    `json:"repo_url"`
	Ref         string    `json:"ref"`
	After       string    `json:"after"`
	Branch      string    `json:"branch"`
	TriggeredBy string    `json:"triggered_by"`
	Body        string    `json:"body"`
	Signature   string    `json:"signature"`
	ReceivedAt  time.Time `json:"received_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Queue struct {
	mu      sync.Mutex
	changed chan struct{}
	now     func() time.Time
	items   []Envelope
}

func NewQueue() *Queue {
	return &Queue{changed: make(chan struct{}), now: time.Now}
}

func (q *Queue) Enqueue(env Envelope) error {
	if env.ProjectID == "" || env.ServiceID == "" {
		return errors.New("project_id and service_id are required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
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
