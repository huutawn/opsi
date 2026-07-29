package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type AuthVerifier interface {
	VerifyAuth(ctx context.Context, auth AuthContext) (AuthContext, error)
}

type HTTPAuthVerifier struct {
	Endpoint string
	Client   *http.Client
	CacheTTL time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	Auth      AuthContext
	ExpiresAt time.Time
}

func (v *HTTPAuthVerifier) VerifyAuth(ctx context.Context, auth AuthContext) (AuthContext, error) {
	if auth.PAT == "" || auth.ProjectID == "" {
		return AuthContext{}, fmt.Errorf("PAT and project_id are required")
	}
	key := authCacheKey(auth.ProjectID, auth.PAT)
	now := v.now()
	if cached, ok := v.cached(key, now); ok {
		cached.PAT = auth.PAT
		return cached, nil
	}

	endpoint := strings.TrimRight(v.Endpoint, "/")
	if endpoint == "" {
		return AuthContext{}, fmt.Errorf("cloud auth endpoint is required")
	}
	body, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
	}{ProjectID: auth.ProjectID})
	if err != nil {
		return AuthContext{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/auth/pat/verify", bytes.NewReader(body))
	if err != nil {
		return AuthContext{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+auth.PAT)
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return AuthContext{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AuthContext{}, fmt.Errorf("cloud auth status %d", resp.StatusCode)
	}
	var result struct {
		UserID    string `json:"user_id"`
		ProjectID string `json:"project_id"`
		Role      string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AuthContext{}, err
	}
	if result.UserID == "" || result.ProjectID == "" || result.Role == "" {
		return AuthContext{}, fmt.Errorf("cloud auth response is incomplete")
	}
	if result.ProjectID != auth.ProjectID {
		return AuthContext{}, fmt.Errorf("cloud auth project mismatch")
	}
	role, err := verifiedRole(result.Role)
	if err != nil {
		return AuthContext{}, err
	}
	verified := auth
	verified.UserID = result.UserID
	verified.ProjectID = result.ProjectID
	verified.Role = role
	v.store(key, verified, now)
	return verified, nil
}

func authCacheKey(projectID, pat string) string {
	digest := sha256.Sum256([]byte(projectID + "\x00" + pat))
	return string(digest[:])
}

func verifiedRole(value string) (Role, error) {
	switch {
	case strings.EqualFold(value, string(RoleOwner)):
		return RoleOwner, nil
	case strings.EqualFold(value, string(RoleDeveloper)):
		return RoleDeveloper, nil
	case strings.EqualFold(value, string(RoleViewer)):
		return RoleViewer, nil
	default:
		return "", fmt.Errorf("cloud auth response role is invalid")
	}
}

func (v *HTTPAuthVerifier) cached(key string, now time.Time) (AuthContext, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.cache[key]
	if !ok || !now.Before(entry.ExpiresAt) {
		return AuthContext{}, false
	}
	return entry.Auth, true
}

func (v *HTTPAuthVerifier) store(key string, auth AuthContext, now time.Time) {
	ttl := v.CacheTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = map[string]cacheEntry{}
	}
	auth.PAT = ""
	v.cache[key] = cacheEntry{Auth: auth, ExpiresAt: now.Add(ttl)}
}

func (v *HTTPAuthVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}
