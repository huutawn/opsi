package adminbootstrap

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
)

const (
	CodeIdentityLinkRequired  = "ADMIN_IDENTITY_LINK_REQUIRED"
	CodeRequiresPostgres      = "ADMIN_BOOTSTRAP_REQUIRES_POSTGRES"
	CodeNotInitialized        = "ADMIN_BOOTSTRAP_NOT_INITIALIZED"
	CodeAlreadyInitialized    = "ADMIN_BOOTSTRAP_ALREADY_INITIALIZED"
	CodeConflict              = "ADMIN_BOOTSTRAP_CONFLICT"
	CodeOAuthIdentityConflict = "OAUTH_IDENTITY_CONFLICT"
	CodeProjectOwnerConflict  = "PROJECT_OWNER_CONFLICT"
	CodePATOutputUnavailable  = "ADMIN_PAT_OUTPUT_UNAVAILABLE"
	bootstrapStateKey         = "first_owner"
	bootstrapPATPurpose       = "bootstrap-owner"
	maxEmailLength            = 320
	maxNameLength             = 120
	maxSlugLength             = 63
	maxOAuthSubjectLength     = 255
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func ErrorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

type Request struct {
	Email             string
	DisplayName       string
	OrgName           string
	OrgSlug           string
	ProjectName       string
	ProjectSlug       string
	OAuthProvider     string
	OAuthSubject      string
	LinkExistingOwner bool
	IssuePAT          bool
	PATTokenHash      string
}

type Result struct {
	UserID                string `json:"user_id"`
	OrganizationID        string `json:"organization_id"`
	ProjectID             string `json:"project_id"`
	MembershipRole        string `json:"role"`
	UserCreated           bool   `json:"user_created,omitempty"`
	OrgCreated            bool   `json:"organization_created,omitempty"`
	ProjectCreated        bool   `json:"project_created,omitempty"`
	MembershipCreated     bool   `json:"membership_created,omitempty"`
	OAuthLinked           bool   `json:"oauth_linked"`
	PATCreated            bool   `json:"pat_created"`
	Reused                bool   `json:"reused"`
	InitialPATUnavailable bool   `json:"-"`
}

func NormalizeAndValidate(req Request, configuredProvider string) (Request, error) {
	if req.LinkExistingOwner {
		if strings.TrimSpace(req.Email) != "" || strings.TrimSpace(req.DisplayName) != "" || strings.TrimSpace(req.OrgName) != "" || strings.TrimSpace(req.OrgSlug) != "" || strings.TrimSpace(req.ProjectName) != "" || strings.TrimSpace(req.ProjectSlug) != "" || req.IssuePAT {
			return Request{}, &Error{Code: CodeConflict, Message: "link-existing-owner accepts only OAuth identity flags"}
		}
		var err error
		req.OAuthProvider, req.OAuthSubject, err = normalizeOAuthIdentity(req.OAuthProvider, req.OAuthSubject, configuredProvider)
		if err != nil {
			return Request{}, err
		}
		if req.OAuthProvider == "" {
			return Request{}, &Error{Code: CodeIdentityLinkRequired, Message: "oauth-provider and oauth-subject are required"}
		}
		return req, nil
	}
	var err error
	if req.Email, err = normalizeEmail(req.Email); err != nil {
		return Request{}, err
	}
	if req.DisplayName, err = cleanText("display name", req.DisplayName, maxNameLength, true); err != nil {
		return Request{}, err
	}
	if req.OrgName, err = cleanText("organization name", req.OrgName, maxNameLength, false); err != nil {
		return Request{}, err
	}
	if req.ProjectName, err = cleanText("project name", req.ProjectName, maxNameLength, false); err != nil {
		return Request{}, err
	}
	if req.OrgSlug, err = normalizeSlug(req.OrgSlug, req.OrgName); err != nil {
		return Request{}, fmt.Errorf("organization slug: %w", err)
	}
	if req.ProjectSlug, err = normalizeSlug(req.ProjectSlug, req.ProjectName); err != nil {
		return Request{}, fmt.Errorf("project slug: %w", err)
	}
	req.OAuthProvider, req.OAuthSubject, err = normalizeOAuthIdentity(req.OAuthProvider, req.OAuthSubject, configuredProvider)
	if err != nil {
		return Request{}, err
	}
	if req.OAuthProvider == "" && !req.IssuePAT {
		return Request{}, &Error{Code: CodeIdentityLinkRequired, Message: "OAuth linkage or pat-output-file is required"}
	}
	return req, nil
}

func normalizeOAuthIdentity(provider, subject, configuredProvider string) (string, string, error) {
	if containsControl(provider) || containsControl(subject) {
		return "", "", &Error{Code: CodeConflict, Message: "OAuth identity is invalid"}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	if (provider == "") != (subject == "") {
		return "", "", &Error{Code: CodeIdentityLinkRequired, Message: "oauth-provider and oauth-subject must be supplied together"}
	}
	if provider == "" {
		return "", "", nil
	}
	configuredProvider = strings.ToLower(strings.TrimSpace(configuredProvider))
	if configuredProvider == "" || provider != configuredProvider {
		return "", "", &Error{Code: CodeConflict, Message: "OAuth provider is not configured by Cloud"}
	}
	if len(subject) > maxOAuthSubjectLength {
		return "", "", &Error{Code: CodeConflict, Message: "OAuth subject is invalid"}
	}
	return provider, subject, nil
}

func normalizeEmail(value string) (string, error) {
	if containsControl(value) {
		return "", &Error{Code: CodeConflict, Message: "email is required and must be a valid address"}
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxEmailLength {
		return "", &Error{Code: CodeConflict, Message: "email is required and must be a valid address"}
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", &Error{Code: CodeConflict, Message: "email must contain one address without a display name"}
	}
	return strings.ToLower(parsed.Address), nil
}

func cleanText(field, value string, limit int, optional bool) (string, error) {
	if containsControl(value) {
		return "", &Error{Code: CodeConflict, Message: field + " is invalid"}
	}
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return "", nil
	}
	if value == "" || len(value) > limit {
		return "", &Error{Code: CodeConflict, Message: field + " is invalid"}
	}
	return value, nil
}

func normalizeSlug(value, name string) (string, error) {
	if containsControl(value) {
		return "", &Error{Code: CodeConflict, Message: "must not contain control characters"}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = slugify(name)
	}
	if len(value) > maxSlugLength || !slugPattern.MatchString(value) {
		return "", &Error{Code: CodeConflict, Message: "must be lowercase letters, numbers, and single hyphens"}
	}
	return value, nil
}

func slugify(value string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		default:
			separator = b.Len() > 0
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
