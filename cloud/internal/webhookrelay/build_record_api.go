package webhookrelay

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

const (
	maxBuildRecordBodyBytes     = 64 << 10
	buildRecordMaxConcurrency   = 8
	buildRecordGlobalLimit      = 120
	buildRecordTokenLimit       = 30
	buildRecordRateWindow       = time.Minute
	buildRecordRateRetrySeconds = 60
)

func (s *Server) handleBuildRecordSubmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "OIDC_REQUEST_INVALID", Message: "OIDC submission does not accept query parameters or cookies", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	release, ok := s.admitBuildRecordSubmission(w, r)
	if !ok {
		return
	}
	defer release()
	token := bearerToken(r)
	if len(r.Header.Values("Authorization")) != 1 || token == "" || strings.ContainsAny(token, " \t\r\n") {
		writeRegistryError(w, registry.APIError{Status: 401, Code: "OIDC_AUTH_REQUIRED", Message: "Authorization bearer OIDC token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if s.OIDC == nil || s.oidcInitError != nil {
		writeRegistryError(w, registry.APIError{Status: 503, Code: "OIDC_UNAVAILABLE", Message: "OIDC verification is unavailable", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	identity, err := s.OIDC.Verify(r.Context(), token)
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: 401, Code: "OIDC_AUTH_INVALID", Message: "OIDC token is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	var submission buildrecordv1.Submission
	if !decodeStrictBuildRecordJSON(w, r, &submission) {
		return
	}
	record, reused, err := s.BuildRecords.Submit(r.Context(), identity, submission)
	if err != nil {
		writeBuildRecordFailure(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"record": record, "reused": reused})
}

func (s *Server) admitBuildRecordSubmission(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if s.buildRecordSlots == nil {
		s.buildRecordSlots = make(chan struct{}, buildRecordMaxConcurrency)
	}
	select {
	case s.buildRecordSlots <- struct{}{}:
	case <-r.Context().Done():
		return func() {}, false
	default:
		writeBuildRecordRateLimit(w, r, "BUILD_RECORD_BUSY", "BuildRecord submission concurrency is saturated", 1)
		return func() {}, false
	}
	release := func() { <-s.buildRecordSlots }
	if s.limits == nil || !s.limits.Allow("build-record:global", buildRecordGlobalLimit, buildRecordRateWindow) {
		release()
		writeBuildRecordRateLimit(w, r, "BUILD_RECORD_RATE_LIMITED", "BuildRecord submission rate limit exceeded", buildRecordRateRetrySeconds)
		return func() {}, false
	}
	token := bearerToken(r)
	if token != "" {
		if !s.limits.Allow("build-record:token:"+tokenHash(token), buildRecordTokenLimit, buildRecordRateWindow) {
			release()
			writeBuildRecordRateLimit(w, r, "BUILD_RECORD_RATE_LIMITED", "BuildRecord submission rate limit exceeded", buildRecordRateRetrySeconds)
			return func() {}, false
		}
	}
	return release, true
}

func writeBuildRecordRateLimit(w http.ResponseWriter, r *http.Request, code, message string, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID")})
}

func (s *Server) handleBuildRecordRead(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "build-records" {
		return false
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	if !s.requireRole(w, r, principal, projectID, "build_record", projectID, "owner", "admin", "developer", "viewer", "support") {
		return true
	}
	if len(parts) == 3 {
		limit, err := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), "50"))
		if err != nil {
			writeRegistryError(w, registry.APIError{Status: 400, Code: "BUILD_RECORD_LIST_INVALID", Message: "limit is invalid", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		repositoryID := uint64(0)
		if raw := r.URL.Query().Get("repository_id"); raw != "" {
			repositoryID, err = strconv.ParseUint(raw, 10, 64)
			if err != nil || repositoryID == 0 {
				writeRegistryError(w, registry.APIError{Status: 400, Code: "BUILD_RECORD_LIST_INVALID", Message: "repository_id is invalid", RequestID: r.Header.Get("X-Request-ID")})
				return true
			}
		}
		result, err := s.BuildRecords.List(r.Context(), projectID, buildrecord.ListFilter{ServiceKey: r.URL.Query().Get("service_key"), RepositoryID: repositoryID, SHA: r.URL.Query().Get("sha"), Status: r.URL.Query().Get("status"), Limit: limit, Cursor: r.URL.Query().Get("cursor")})
		if err != nil {
			writeBuildRecordFailure(w, r, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
		return true
	}
	if len(parts) == 4 {
		record, err := s.BuildRecords.Get(r.Context(), projectID, parts[3])
		if err != nil {
			writeBuildRecordFailure(w, r, err)
			return true
		}
		writeJSON(w, http.StatusOK, record)
		return true
	}
	http.NotFound(w, r)
	return true
}

func decodeStrictBuildRecordJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBuildRecordBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "build record request body is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "build record request body must contain one JSON value", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func writeBuildRecordFailure(w http.ResponseWriter, r *http.Request, err error) {
	var typed buildrecord.Error
	if errors.As(err, &typed) {
		writeRegistryError(w, registry.APIError{Status: typed.Status, Code: typed.Code, Message: typed.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeRegistryError(w, registry.APIError{Status: 500, Code: "INTERNAL", Message: "Internal server error.", RequestID: r.Header.Get("X-Request-ID")})
}
