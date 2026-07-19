package githuboidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	GitHubIssuer         = "https://token.actions.githubusercontent.com"
	GitHubJWKS           = "https://token.actions.githubusercontent.com/.well-known/jwks"
	DefaultAudience      = "https://github.com/huutawn/opsi"
	maxDefaultTokenBytes = 16 << 10
	maxDefaultJWKSBytes  = 256 << 10
	maxDefaultJWKKeys    = 32
)

var (
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	idPattern  = regexp.MustCompile(`^[0-9]{1,20}$`)
)

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

type WorkloadPolicy struct {
	RepositoryID    uint64   `json:"repository_id"`
	ServiceKey      string   `json:"service_key"`
	WorkflowRefs    []string `json:"workflow_refs"`
	JobWorkflowRefs []string `json:"job_workflow_refs,omitempty"`
	Refs            []string `json:"refs"`
	Events          []string `json:"events"`
	OCIRepositories []string `json:"oci_repositories"`
}

type Config struct {
	Enabled       bool             `json:"enabled"`
	Issuer        string           `json:"issuer"`
	JWKSURL       string           `json:"jwks_url"`
	Audience      string           `json:"audience"`
	ClockSkew     Duration         `json:"clock_skew"`
	HTTPTimeout   Duration         `json:"http_timeout"`
	MaxTokenBytes int              `json:"max_token_bytes"`
	MaxJWKSBytes  int              `json:"max_jwks_bytes"`
	MaxJWKKeys    int              `json:"max_jwk_keys"`
	CacheTTL      Duration         `json:"cache_ttl"`
	Workloads     []WorkloadPolicy `json:"workloads"`
}

func DefaultConfig() Config {
	return Config{Issuer: GitHubIssuer, JWKSURL: GitHubJWKS, Audience: DefaultAudience,
		ClockSkew: Duration(time.Minute), HTTPTimeout: Duration(5 * time.Second),
		MaxTokenBytes: maxDefaultTokenBytes, MaxJWKSBytes: maxDefaultJWKSBytes,
		MaxJWKKeys: maxDefaultJWKKeys, CacheTTL: Duration(15 * time.Minute)}
}

func (c Config) Validate(production bool) error {
	if c.Issuer != GitHubIssuer || c.JWKSURL != GitHubJWKS {
		return errors.New("GitHub OIDC issuer and JWKS URL must be the pinned production endpoints")
	}
	if _, err := exactHTTPSURL(c.Issuer); err != nil {
		return errors.New("GitHub OIDC issuer must be exact HTTPS")
	}
	if _, err := exactHTTPSURL(c.JWKSURL); err != nil {
		return errors.New("GitHub OIDC JWKS URL must be exact HTTPS")
	}
	if strings.TrimSpace(c.Audience) == "" || len(c.Audience) > 256 || !safeValue(c.Audience) {
		return errors.New("GitHub OIDC audience must be bounded and printable")
	}
	if time.Duration(c.ClockSkew) < 0 || time.Duration(c.ClockSkew) > 5*time.Minute {
		return errors.New("GitHub OIDC clock skew must be between 0 and 5m")
	}
	if time.Duration(c.HTTPTimeout) <= 0 || time.Duration(c.HTTPTimeout) > 30*time.Second {
		return errors.New("GitHub OIDC HTTP timeout must be between 1ns and 30s")
	}
	if c.MaxTokenBytes < 1024 || c.MaxTokenBytes > 64<<10 || c.MaxJWKSBytes < 4096 || c.MaxJWKSBytes > 1<<20 || c.MaxJWKKeys < 1 || c.MaxJWKKeys > 128 {
		return errors.New("GitHub OIDC token/JWKS bounds are invalid")
	}
	if time.Duration(c.CacheTTL) < time.Minute || time.Duration(c.CacheTTL) > time.Hour {
		return errors.New("GitHub OIDC cache TTL must be between 1m and 1h")
	}
	if production && !c.Enabled {
		return errors.New("production requires GitHub OIDC to be enabled")
	}
	if c.Enabled && len(c.Workloads) == 0 {
		return errors.New("enabled GitHub OIDC requires at least one allowed workload")
	}
	for i, policy := range c.Workloads {
		if policy.RepositoryID == 0 || !validServiceKey(policy.ServiceKey) || len(policy.WorkflowRefs) == 0 || len(policy.Refs) == 0 || len(policy.Events) == 0 || len(policy.OCIRepositories) == 0 {
			return fmt.Errorf("GitHub OIDC workload %d is incomplete", i)
		}
		for _, values := range [][]string{policy.WorkflowRefs, policy.JobWorkflowRefs, policy.Refs, policy.Events, policy.OCIRepositories} {
			for _, value := range values {
				if len(value) == 0 || len(value) > 512 || !safeValue(value) {
					return fmt.Errorf("GitHub OIDC workload %d contains an invalid allowlist value", i)
				}
			}
		}
	}
	return nil
}

type VerifiedIdentity struct {
	Issuer            string
	Subject           string
	Repository        string
	RepositoryID      uint64
	RepositoryOwner   string
	RepositoryOwnerID uint64
	Ref               string
	SHA               string
	EventName         string
	Workflow          string
	WorkflowRef       string
	JobWorkflowRef    string
	RunID             uint64
	RunAttempt        uint32
}

type Clock func() time.Time

type Verifier struct {
	config Config
	http   *http.Client
	now    Clock
	keys   *jwksCache
}

func New(config Config) (*Verifier, error) {
	if err := config.Validate(false); err != nil {
		return nil, err
	}
	return newVerifier(config, &http.Client{Timeout: time.Duration(config.HTTPTimeout), CheckRedirect: noRedirect}, time.Now), nil
}

func newVerifier(config Config, client *http.Client, now Clock) *Verifier {
	if client == nil {
		client = &http.Client{Timeout: time.Duration(config.HTTPTimeout), CheckRedirect: noRedirect}
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = noRedirect
	}
	if now == nil {
		now = time.Now
	}
	return &Verifier{config: config, http: client, now: now, keys: &jwksCache{ttl: time.Duration(config.CacheTTL), maxBytes: config.MaxJWKSBytes, maxKeys: config.MaxJWKKeys, http: client, url: config.JWKSURL, now: now}}
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (v *Verifier) Verify(ctx context.Context, token string) (VerifiedIdentity, error) {
	if !v.config.Enabled {
		return VerifiedIdentity{}, errors.New("OIDC_DISABLED")
	}
	if len(token) == 0 || len(token) > v.config.MaxTokenBytes {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	headerBytes, err := decodeSegment(parts[0], 4096)
	if err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	payloadBytes, err := decodeSegment(parts[1], v.config.MaxTokenBytes)
	if err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	signature, err := decodeSegment(parts[2], 4096)
	if err != nil || len(signature) == 0 {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	var header struct {
		Alg string          `json:"alg"`
		Kid string          `json:"kid"`
		Typ string          `json:"typ"`
		JWK json.RawMessage `json:"jwk"`
		JKU string          `json:"jku"`
		X5U string          `json:"x5u"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Alg != "RS256" || header.Kid == "" || len(header.Kid) > 256 || !safeValue(header.Kid) || len(header.JWK) != 0 || header.JKU != "" || header.X5U != "" {
		return VerifiedIdentity{}, errors.New("OIDC_TOKEN_INVALID")
	}
	keys, err := v.keys.key(ctx, header.Kid)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(keys, crypto.SHA256, hash[:], signature); err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_SIGNATURE_INVALID")
	}
	return v.parseClaims(payloadBytes)
}

func (v *Verifier) parseClaims(data []byte) (VerifiedIdentity, error) {
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(data, &claims); err != nil || claims == nil {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	now := v.now().UTC()
	issuer, err := requiredString(claims, "iss", 256)
	if err != nil || issuer != v.config.Issuer {
		return VerifiedIdentity{}, errors.New("OIDC_ISSUER_INVALID")
	}
	audience, err := requiredString(claims, "aud", 256)
	if err != nil || audience != v.config.Audience {
		return VerifiedIdentity{}, errors.New("OIDC_AUDIENCE_INVALID")
	}
	exp, err := requiredUnix(claims, "exp")
	if err != nil || now.After(exp.Add(time.Duration(v.config.ClockSkew))) {
		return VerifiedIdentity{}, errors.New("OIDC_EXP_INVALID")
	}
	nbf, err := requiredUnix(claims, "nbf")
	if err != nil || now.Add(time.Duration(-v.config.ClockSkew)).Before(nbf) {
		return VerifiedIdentity{}, errors.New("OIDC_NBF_INVALID")
	}
	iat, err := requiredUnix(claims, "iat")
	if err != nil || iat.After(now.Add(time.Duration(v.config.ClockSkew))) {
		return VerifiedIdentity{}, errors.New("OIDC_IAT_INVALID")
	}
	if exp.Before(iat) || nbf.After(exp) || exp.Sub(iat) > time.Hour {
		return VerifiedIdentity{}, errors.New("OIDC_TIME_INVALID")
	}
	identity := VerifiedIdentity{Issuer: issuer}
	if identity.Subject, err = requiredString(claims, "sub", 512); err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.Repository, err = requiredString(claims, "repository", 256); err != nil || !validRepositoryName(identity.Repository) {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.RepositoryOwner, err = requiredString(claims, "repository_owner", 128); err != nil || !safeValue(identity.RepositoryOwner) {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.RepositoryID, err = requiredID(claims, "repository_id"); err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.RepositoryOwnerID, err = requiredID(claims, "repository_owner_id"); err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.RunID, err = requiredID(claims, "run_id"); err != nil {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	var attempt uint64
	if attempt, err = requiredID(claims, "run_attempt"); err != nil || attempt > 1<<31-1 {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	identity.RunAttempt = uint32(attempt)
	for key, limit := range map[string]int{"ref": 512, "workflow": 256, "workflow_ref": 512, "event_name": 128} {
		value, valueErr := requiredString(claims, key, limit)
		if valueErr != nil || !safeValue(value) {
			return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
		}
		switch key {
		case "ref":
			identity.Ref = value
		case "workflow":
			identity.Workflow = value
		case "workflow_ref":
			identity.WorkflowRef = value
		case "event_name":
			identity.EventName = value
		}
	}
	if !validRef(identity.Ref) || !validEvent(identity.EventName) || !validWorkflowRef(identity.WorkflowRef) {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if identity.SHA, err = requiredString(claims, "sha", 64); err != nil || !shaPattern.MatchString(identity.SHA) {
		return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
	}
	if raw := claims["job_workflow_ref"]; len(raw) != 0 && string(raw) != "null" {
		if identity.JobWorkflowRef, err = requiredString(claims, "job_workflow_ref", 512); err != nil || !validWorkflowRef(identity.JobWorkflowRef) {
			return VerifiedIdentity{}, errors.New("OIDC_CLAIMS_INVALID")
		}
	}
	return identity, nil
}

func decodeSegment(value string, max int) ([]byte, error) {
	if len(value) > max*2 {
		return nil, errors.New("segment too large")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) > max {
		return nil, errors.New("segment invalid")
	}
	return decoded, nil
}

func requiredString(claims map[string]json.RawMessage, key string, max int) (string, error) {
	var value string
	raw, ok := claims[key]
	if !ok || json.Unmarshal(raw, &value) != nil || value == "" || len(value) > max || !safeValue(value) {
		return "", errors.New("claim string invalid")
	}
	return value, nil
}

func requiredID(claims map[string]json.RawMessage, key string) (uint64, error) {
	value, err := requiredString(claims, key, 20)
	if err != nil || !idPattern.MatchString(value) {
		return 0, errors.New("claim id invalid")
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 || id > uint64(1<<63-1) {
		return 0, errors.New("claim id invalid")
	}
	return id, nil
}

func requiredUnix(claims map[string]json.RawMessage, key string) (time.Time, error) {
	var value int64
	raw, ok := claims[key]
	if !ok || json.Unmarshal(raw, &value) != nil || value <= 0 || value > 1<<34 {
		return time.Time{}, errors.New("claim time invalid")
	}
	return time.Unix(value, 0).UTC(), nil
}

func exactHTTPSURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("url invalid")
	}
	return u, nil
}

func safeValue(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}
func validServiceKey(value string) bool {
	return len(value) <= 128 && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(value)
}
func validRepositoryName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && safeValue(parts[0]) && safeValue(parts[1]) && len(parts[0]) <= 128 && len(parts[1]) <= 128
}
func validRef(value string) bool {
	for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/pull/"} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}
func validEvent(value string) bool {
	return len(value) <= 128 && regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`).MatchString(value)
}
func validWorkflowRef(value string) bool {
	return strings.Contains(value, "/.github/workflows/") && strings.Contains(value, "@refs/") && len(value) <= 512
}
