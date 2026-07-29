package incident

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

const (
	IncidentEvidenceSchemaVersion = "opsi.incident_evidence.v1"
	MaxIncidentEvidenceBytes      = 256 << 10
	MaxTimelineEntries            = 128
	MaxKubernetesEventEntries     = 64
	MaxLogFingerprintGroups       = 64
	MaxRedactedExcerptBytes       = 512
	MaxAuditReferences            = 64
	MaxEvidenceSourceWindow       = 30 * time.Minute
	MaxKubernetesCommandDuration  = 5 * time.Second
	MaxEvidenceOperationDuration  = 30 * time.Second
	maxEvidencePodEntries         = 64
)

var (
	ErrEvidenceCorrupt             = errors.New("stored incident evidence is invalid")
	ErrEvidenceUnavailable         = errors.New("incident evidence is unavailable")
	ErrEvidenceTooLarge            = errors.New("incident evidence exceeds the size limit")
	evidenceIPv4Pattern            = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	evidenceIPv6Pattern            = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}\b`)
	evidenceCredentialPattern      = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+\S+|(?:password|passwd|pwd|token|pat|api[_-]?key|secret|otp|registry credential)\s*[:=]\s*\S+)`)
	evidencePrivateKeyPattern      = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
	evidenceForbiddenPhrasePattern = regexp.MustCompile(`(?i)(kubeconfig|registry credential)`)
)

type incidentEvidencePersistence interface {
	PersistIncidentEvidence(ctx context.Context, projectID, incidentID, body, hash string, generatedAt time.Time) error
}

type telemetryEvidenceSource interface {
	IncidentEvidenceTelemetry(ctx context.Context, projectID, nodeID, serviceID, podID string, since, until time.Time, limit int) (telemetry.IncidentEvidenceTelemetry, error)
}

type auditEvidenceSource interface {
	EvidenceAuditRecords(ctx context.Context, projectID, incidentID, serviceID string, since, until time.Time, limit int) ([]telemetry.EvidenceAuditRecord, int, error)
}

type rolloutEvidenceSource interface {
	ReadIncidentEvidence(ctx context.Context, projectID, serviceID string, since, until time.Time) (*deploy.EvidenceProjection, error)
}

type kubernetesEvidenceSource interface {
	Read(ctx context.Context, projectID, serviceID, nodeID, podID string) (KubernetesEvidenceResult, error)
}

func EncodeIncidentEvidence(evidence *agentv1.IncidentEvidence) ([]byte, error) {
	if evidence == nil || evidence.SchemaVersion != IncidentEvidenceSchemaVersion {
		return nil, ErrEvidenceCorrupt
	}
	payload := *evidence
	payload.ContentSHA256 = ""
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrEvidenceCorrupt
	}
	sum := sha256.Sum256(canonical)
	evidence.ContentSHA256 = hex.EncodeToString(sum[:])
	body, err := json.Marshal(evidence)
	if err != nil {
		return nil, ErrEvidenceCorrupt
	}
	if len(body) > MaxIncidentEvidenceBytes {
		return nil, ErrEvidenceTooLarge
	}
	if containsEvidenceSecret(string(body)) {
		return nil, errors.New("incident evidence contains sensitive content")
	}
	return body, nil
}

func VerifyIncidentEvidence(body []byte, storedHash string) (*agentv1.IncidentEvidence, error) {
	if len(body) == 0 || len(body) > MaxIncidentEvidenceBytes || len(storedHash) != 64 {
		return nil, ErrEvidenceCorrupt
	}
	var evidence agentv1.IncidentEvidence
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil || evidence.SchemaVersion != IncidentEvidenceSchemaVersion || evidence.ContentSHA256 != storedHash {
		return nil, ErrEvidenceCorrupt
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrEvidenceCorrupt
	}
	payload := evidence
	payload.ContentSHA256 = ""
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrEvidenceCorrupt
	}
	sum := sha256.Sum256(canonical)
	canonicalBody, err := json.Marshal(&evidence)
	if err != nil || !bytes.Equal(canonicalBody, body) || hex.EncodeToString(sum[:]) != storedHash || containsEvidenceSecret(string(body)) {
		return nil, ErrEvidenceCorrupt
	}
	return &evidence, nil
}

func safeEvidenceText(value string, limit int) string {
	value = telemetry.RedactSensitiveText(value)
	value = evidencePrivateKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = evidenceCredentialPattern.ReplaceAllString(value, "[REDACTED]")
	value = evidenceForbiddenPhrasePattern.ReplaceAllString(value, "[REDACTED]")
	value = evidenceIPv4Pattern.ReplaceAllString(value, "[REDACTED_ADDRESS]")
	value = evidenceIPv6Pattern.ReplaceAllString(value, "[REDACTED_ADDRESS]")
	return truncateUTF8(value, limit)
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func containsEvidenceSecret(value string) bool {
	lower := strings.ToLower(value)
	return evidencePrivateKeyPattern.MatchString(value) || evidenceCredentialPattern.MatchString(value) ||
		strings.Contains(lower, "-----begin private key-----") || strings.Contains(lower, "kubeconfig") ||
		strings.Contains(lower, "registry credential")
}

func canonicalizeEvidence(evidence *agentv1.IncidentEvidence) {
	sort.Slice(evidence.Timeline, func(i, j int) bool {
		left, right := evidence.Timeline[i], evidence.Timeline[j]
		if left.ObservedAtUnix != right.ObservedAtUnix {
			return left.ObservedAtUnix < right.ObservedAtUnix
		}
		return left.Source+"\x00"+left.Kind+"\x00"+left.Detail < right.Source+"\x00"+right.Kind+"\x00"+right.Detail
	})
	sort.Slice(evidence.Pods, func(i, j int) bool {
		return evidence.Pods[i].Namespace+"\x00"+evidence.Pods[i].PodID < evidence.Pods[j].Namespace+"\x00"+evidence.Pods[j].PodID
	})
	sort.Slice(evidence.KubernetesEvents, func(i, j int) bool {
		left, right := evidence.KubernetesEvents[i], evidence.KubernetesEvents[j]
		if left.ObservedAtUnix != right.ObservedAtUnix {
			return left.ObservedAtUnix < right.ObservedAtUnix
		}
		return left.Namespace+"\x00"+left.ObjectName+"\x00"+left.Reason < right.Namespace+"\x00"+right.ObjectName+"\x00"+right.Reason
	})
	sort.Slice(evidence.LogFingerprints, func(i, j int) bool {
		return evidence.LogFingerprints[i].Fingerprint < evidence.LogFingerprints[j].Fingerprint
	})
	sort.Slice(evidence.AuditReferences, func(i, j int) bool {
		if evidence.AuditReferences[i].CreatedAtUnix != evidence.AuditReferences[j].CreatedAtUnix {
			return evidence.AuditReferences[i].CreatedAtUnix < evidence.AuditReferences[j].CreatedAtUnix
		}
		return evidence.AuditReferences[i].AuditID < evidence.AuditReferences[j].AuditID
	})
	sort.Slice(evidence.Coverage, func(i, j int) bool { return evidence.Coverage[i].Source < evidence.Coverage[j].Source })
	sort.Slice(evidence.Truncations, func(i, j int) bool { return evidence.Truncations[i].Section < evidence.Truncations[j].Section })
}

func limitEvidenceSection[T any](values []T, limit int, section string, evidence *agentv1.IncidentEvidence) []T {
	if len(values) <= limit {
		return values
	}
	evidence.Truncations = append(evidence.Truncations, agentv1.IncidentTruncation{Section: section, OmittedItems: int32(len(values) - limit), UTF8Safe: true})
	for index := range evidence.Coverage {
		if evidence.Coverage[index].Source == section {
			evidence.Coverage[index].Status = "truncated"
			evidence.Coverage[index].Truncated = true
		}
	}
	return values[:limit]
}
