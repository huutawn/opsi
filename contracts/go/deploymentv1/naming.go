package deploymentv1

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var unsafeDNSLabelPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// StableDNSName returns the Kubernetes name used by the Agent for a workload.
func StableDNSName(prefix string, identities ...string) string {
	parts := []string{safeDNSLabel(prefix)}
	for _, identity := range identities {
		part := safeDNSLabel(identity)
		if len(part) > 18 {
			part = part[:18]
		}
		parts = append(parts, strings.Trim(part, "-"))
	}
	base := strings.Trim(strings.Join(parts, "-"), "-")
	sum := sha256.Sum256([]byte(strings.Join(identities, "\x00")))
	suffix := hex.EncodeToString(sum[:])[:10]
	if len(base) > 52 {
		base = strings.TrimRight(base[:52], "-")
	}
	if base == "" {
		base = "opsi"
	}
	return base + "-" + suffix
}

func safeDNSLabel(value string) string {
	value = unsafeDNSLabelPattern.ReplaceAllString(strings.ToLower(value), "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
