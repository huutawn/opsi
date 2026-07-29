package githuboidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

type jwksCache struct {
	mu                 sync.Mutex
	keys               map[string]*rsa.PublicKey
	expiresAt          time.Time
	lastUnknownRefresh time.Time
	retryAfter         time.Time
	refreshing         bool
	wait               chan struct{}
	ttl                time.Duration
	maxBytes           int
	maxKeys            int
	http               *http.Client
	url                string
	now                Clock
}

func (c *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	for attempt := 0; attempt < 3; attempt++ {
		now := c.now().UTC()
		c.mu.Lock()
		key, found := c.keys[kid]
		cacheFresh := len(c.keys) > 0 && now.Before(c.expiresAt)
		if found && cacheFresh {
			c.mu.Unlock()
			return key, nil
		}
		wasUnknown := !found && cacheFresh
		unknownRefresh := wasUnknown && (c.lastUnknownRefresh.IsZero() || now.Sub(c.lastUnknownRefresh) >= time.Minute)
		if !cacheFresh && now.Before(c.retryAfter) {
			c.mu.Unlock()
			return nil, errors.New("OIDC_JWKS_UNAVAILABLE")
		}
		wait := c.wait
		if c.refreshing {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, errors.New("OIDC_JWKS_UNAVAILABLE")
			case <-wait:
			}
			continue
		}
		if !found && !unknownRefresh && cacheFresh {
			c.mu.Unlock()
			return nil, errors.New("OIDC_KID_UNKNOWN")
		}
		c.refreshing = true
		c.wait = make(chan struct{})
		wait = c.wait
		c.mu.Unlock()

		keys, err := c.fetch(ctx)
		c.mu.Lock()
		if wasUnknown {
			c.lastUnknownRefresh = now
		}
		if err == nil {
			c.keys, c.expiresAt = keys, now.Add(c.ttl)
			c.retryAfter = time.Time{}
		} else {
			c.retryAfter = now.Add(time.Minute)
		}
		c.refreshing = false
		close(wait)
		c.wait = nil
		key = c.keys[kid]
		cacheFresh = key != nil && now.Before(c.expiresAt)
		c.mu.Unlock()
		if err != nil {
			return nil, errors.New("OIDC_JWKS_UNAVAILABLE")
		}
		if !cacheFresh {
			return nil, errors.New("OIDC_KID_UNKNOWN")
		}
		return key, nil
	}
	return nil, errors.New("OIDC_JWKS_UNAVAILABLE")
}

func (c *jwksCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		return nil, errors.New("jwks request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(c.maxBytes)+1))
	if err != nil || len(data) > c.maxBytes {
		return nil, errors.New("jwks body invalid")
	}
	var envelope struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			D   string `json:"d"`
			P   string `json:"p"`
			Q   string `json:"q"`
		} `json:"keys"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Keys) == 0 || len(envelope.Keys) > c.maxKeys {
		return nil, errors.New("jwks JSON invalid")
	}
	result := make(map[string]*rsa.PublicKey, len(envelope.Keys))
	for _, item := range envelope.Keys {
		if item.Kty != "RSA" || item.Use != "sig" || item.Alg != "RS256" || item.Kid == "" || item.D != "" || item.P != "" || item.Q != "" || len(result) >= c.maxKeys {
			return nil, errors.New("jwks key invalid")
		}
		key, err := modulusToPublicKey(item.N, item.E)
		if err != nil {
			return nil, err
		}
		if _, exists := result[item.Kid]; exists {
			return nil, errors.New("jwks kid duplicate")
		}
		result[item.Kid] = key
	}
	return result, nil
}

func modulusToPublicKey(n, e string) (*rsa.PublicKey, error) {
	modulus, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil || len(modulus) < 256 || len(modulus) > 1024 {
		return nil, errors.New("modulus invalid")
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, errors.New("exponent invalid")
	}
	exponent := new(big.Int).SetBytes(exponentBytes)
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64()%2 == 0 {
		return nil, errors.New("exponent invalid")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exponent.Int64())}, nil
}
