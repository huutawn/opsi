package telemetry

import (
	"regexp"
	"strings"
)

var sensitiveLogPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s,;]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(password|passwd|pwd|token|pat|api[_-]?key|secret|authorization|bearer)\s*[:=]\s*("[^"]+"|'[^']+'|[^\s,;]+)`), `$1=[REDACTED]`},
	{regexp.MustCompile(`-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----`), `[REDACTED_PRIVATE_KEY]`},
}

func RedactSensitiveText(value string) string {
	out := value
	for _, pattern := range sensitiveLogPatterns {
		out = pattern.re.ReplaceAllString(out, pattern.repl)
	}
	out = strings.ReplaceAll(out, "kubeconfig", "[REDACTED]")
	return out
}
