package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var volatileTokenPattern = regexp.MustCompile(`\b([0-9a-f]{8,}|\d{2,})\b`)

func Fingerprint(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	normalized = volatileTokenPattern.ReplaceAllString(normalized, "*")
	normalized = strings.Join(strings.Fields(normalized), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
