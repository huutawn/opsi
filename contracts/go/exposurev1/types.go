// Package exposurev1 defines the canonical external exposure contract.
package exposurev1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SchemaVersion = "opsi.exposure_spec/v1"
	TLSDisabled   = "disabled"
	TLSSecretRef  = "secret_ref"

	MaxJSONBytes = 32 * 1024
	MaxPathBytes = 2048
)

const (
	CodeInvalidJSON          = "EXPOSURE_INVALID_JSON"
	CodeInvalidSchemaVersion = "EXPOSURE_INVALID_SCHEMA_VERSION"
	CodeInvalidIdentity      = "EXPOSURE_INVALID_IDENTITY"
	CodeInvalidHostname      = "EXPOSURE_INVALID_HOSTNAME"
	CodeInvalidPath          = "EXPOSURE_INVALID_PATH"
	CodeInvalidPort          = "EXPOSURE_INVALID_SERVICE_PORT"
	CodeInvalidTLS           = "EXPOSURE_INVALID_TLS"
	CodeInvalidMetadata      = "EXPOSURE_INVALID_METADATA"
	CodeSpecHashMismatch     = "EXPOSURE_SPEC_HASH_MISMATCH"
)

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidationError is safe to return across contract boundaries.
type ValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Field + ": " + e.Message
}

type TLSConfig struct {
	Mode            string `json:"mode"`
	SecretReference string `json:"secret_ref,omitempty"`
}

type Metadata struct {
	DisplayName string `json:"display_name,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

type ExposureSpec struct {
	SchemaVersion   string    `json:"schema_version"`
	ProjectID       string    `json:"project_id"`
	EnvironmentID   string    `json:"environment_id"`
	RuntimeID       string    `json:"runtime_id"`
	ServiceKey      string    `json:"service_key"`
	DeploymentJobID string    `json:"deployment_job_id"`
	Hostname        string    `json:"hostname"`
	Path            string    `json:"path"`
	ServicePort     int32     `json:"service_port"`
	TLS             TLSConfig `json:"tls"`
	Metadata        *Metadata `json:"metadata,omitempty"`
	SpecHash        string    `json:"spec_hash"`
}

// DecodeStrictJSON rejects unknown fields and returns a canonical, hash-checked spec.
func DecodeStrictJSON(data []byte) (ExposureSpec, error) {
	if len(data) == 0 || len(data) > MaxJSONBytes {
		return ExposureSpec{}, validationError(CodeInvalidJSON, "", "JSON document size is outside the allowed bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec ExposureSpec
	if err := decoder.Decode(&spec); err != nil {
		return ExposureSpec{}, validationError(CodeInvalidJSON, "", "JSON document does not match ExposureSpec v1")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ExposureSpec{}, validationError(CodeInvalidJSON, "", "JSON document must contain exactly one value")
	}
	if spec.SpecHash == "" {
		return ExposureSpec{}, validationError(CodeSpecHashMismatch, "spec_hash", "spec_hash is required")
	}
	return spec.Canonicalize()
}

// Canonicalize normalizes hostname/path and computes the authoritative spec hash.
func (s ExposureSpec) Canonicalize() (ExposureSpec, error) {
	out := s
	hostname, err := NormalizeHostname(out.Hostname)
	if err != nil {
		return ExposureSpec{}, err
	}
	out.Hostname = hostname
	canonicalPath, err := NormalizePath(out.Path)
	if err != nil {
		return ExposureSpec{}, err
	}
	out.Path = canonicalPath
	if out.Metadata != nil && out.Metadata.DisplayName == "" && out.Metadata.Rationale == "" {
		out.Metadata = nil
	}
	if err := out.validateShape(); err != nil {
		return ExposureSpec{}, err
	}
	hash, err := out.RuntimeHash()
	if err != nil {
		return ExposureSpec{}, err
	}
	if s.SpecHash != "" && s.SpecHash != hash {
		// R5-011.1 included display metadata in SpecHash. Accept that legacy
		// representation once, then canonicalize to the runtime-only hash so a
		// display edit cannot trigger a Kubernetes mutation or rollback.
		legacyHash, legacyErr := out.legacyHash()
		if legacyErr != nil || s.SpecHash != legacyHash {
			return ExposureSpec{}, validationError(CodeSpecHashMismatch, "spec_hash", "spec_hash does not match the canonical contract")
		}
	}
	out.SpecHash = hash
	return out, nil
}

func (s ExposureSpec) Validate() error {
	if s.SpecHash == "" {
		return validationError(CodeSpecHashMismatch, "spec_hash", "spec_hash is required")
	}
	_, err := s.Canonicalize()
	return err
}

func (s ExposureSpec) Hash() (string, error) {
	canonical, err := s.Canonicalize()
	if err != nil {
		return "", err
	}
	return canonical.SpecHash, nil
}

// RuntimeHash identifies only fields that can change the rendered Ingress or
// its workload binding. Metadata is intentionally excluded from this hash.
func (s ExposureSpec) RuntimeHash() (string, error) {
	canonical := s
	canonical.SpecHash = ""
	canonical.Metadata = nil
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal runtime ExposureSpec: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s ExposureSpec) validateShape() error {
	if s.SchemaVersion != SchemaVersion {
		return validationError(CodeInvalidSchemaVersion, "schema_version", "unsupported ExposureSpec schema version")
	}
	identities := []struct {
		field string
		value string
	}{
		{field: "project_id", value: s.ProjectID},
		{field: "environment_id", value: s.EnvironmentID},
		{field: "runtime_id", value: s.RuntimeID},
		{field: "service_key", value: s.ServiceKey},
		{field: "deployment_job_id", value: s.DeploymentJobID},
	}
	for _, identity := range identities {
		if !opaqueIDPattern.MatchString(identity.value) {
			return validationError(CodeInvalidIdentity, identity.field, "identity must be an opaque bounded identifier")
		}
	}
	if s.ServicePort < 1 || s.ServicePort > 65535 {
		return validationError(CodeInvalidPort, "service_port", "service_port must be between 1 and 65535")
	}
	switch s.TLS.Mode {
	case TLSDisabled:
		if s.TLS.SecretReference != "" {
			return validationError(CodeInvalidTLS, "tls.secret_ref", "disabled TLS must not include a secret reference")
		}
	case TLSSecretRef:
		if !opaqueIDPattern.MatchString(s.TLS.SecretReference) {
			return validationError(CodeInvalidTLS, "tls.secret_ref", "TLS secret reference must be an opaque bounded identifier")
		}
	default:
		return validationError(CodeInvalidTLS, "tls.mode", "TLS mode must be disabled or secret_ref")
	}
	if s.Metadata != nil {
		if len(s.Metadata.DisplayName) > 128 || len(s.Metadata.Rationale) > 512 || containsControl(s.Metadata.DisplayName) || containsControl(s.Metadata.Rationale) {
			return validationError(CodeInvalidMetadata, "metadata", "display metadata exceeds its bound or contains control characters")
		}
	}
	return nil
}

func (s ExposureSpec) legacyHash() (string, error) {
	payload := s
	payload.SpecHash = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal legacy ExposureSpec: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func NormalizeHostname(value string) (string, error) {
	if value == "" || len(value) > 253 {
		return "", validationError(CodeInvalidHostname, "hostname", "hostname length is outside DNS bounds")
	}
	for _, r := range value {
		if r > unicode.MaxASCII {
			return "", validationError(CodeInvalidHostname, "hostname", "Unicode and IDNA hostnames are not supported")
		}
	}
	hostname := strings.ToLower(value)
	if hostname != strings.TrimSpace(hostname) || strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return "", validationError(CodeInvalidHostname, "hostname", "hostname must not contain whitespace or a leading/trailing dot")
	}
	if strings.ContainsAny(hostname, "/:?#*") || net.ParseIP(hostname) != nil || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return "", validationError(CodeInvalidHostname, "hostname", "hostname must be a non-local DNS name without scheme, port, path, or wildcard")
	}
	labels := strings.Split(hostname, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", validationError(CodeInvalidHostname, "hostname", "hostname contains an empty or oversized DNS label")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", validationError(CodeInvalidHostname, "hostname", "hostname contains an invalid DNS character")
			}
		}
	}
	return hostname, nil
}

func NormalizePath(value string) (string, error) {
	if value == "" || len(value) > MaxPathBytes || !utf8.ValidString(value) {
		return "", validationError(CodeInvalidPath, "path", "path must be valid UTF-8 within the allowed bound")
	}
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.ContainsAny(value, "?#\\%") || containsControl(value) {
		return "", validationError(CodeInvalidPath, "path", "path must be absolute and must not contain query, fragment, backslash, percent encoding, repeated slash, or control characters")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return "", validationError(CodeInvalidPath, "path", "dot path segments are not allowed")
		}
	}
	if value != "/" && strings.HasSuffix(value, "/") {
		value = strings.TrimSuffix(value, "/")
	}
	return value, nil
}

// PathsConflict implements Kubernetes Prefix path-component semantics.
func PathsConflict(first, second string) bool {
	if first == "/" || second == "/" || first == second {
		return true
	}
	return strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validationError(code, field, message string) error {
	return &ValidationError{Code: code, Field: field, Message: message}
}
