package deploy

import "regexp"

var (
	privateKeyBlockRe = regexp.MustCompile(`(?is)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
	urlUserInfoRe     = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/\s@]+)@`)
	bearerTokenRe     = regexp.MustCompile(`(?i)\bbearer\s+[-._~+/=a-z0-9]+`)
	keyValueSecretRe  = regexp.MustCompile(`(?i)\b(password|passwd|token|secret|api[_-]?key|authorization|pat|otp|totp|database_url)(\s*[:=]\s*)([^\s,;]+)`)
	kubeSecretDataRe  = regexp.MustCompile(`(?i)\b(client-key-data|certificate-authority-data|token)(\s*:\s*)([^\s,;]+)`)
	openAIKeyRe       = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`)
)

func RedactSensitive(s string) string {
	s = privateKeyBlockRe.ReplaceAllString(s, "[REDACTED_PRIVATE_KEY]")
	s = urlUserInfoRe.ReplaceAllString(s, "${1}[REDACTED]@")
	s = bearerTokenRe.ReplaceAllString(s, "Bearer [REDACTED]")
	s = keyValueSecretRe.ReplaceAllString(s, "${1}${2}[REDACTED]")
	s = kubeSecretDataRe.ReplaceAllString(s, "${1}${2}[REDACTED]")
	return openAIKeyRe.ReplaceAllString(s, "[REDACTED_API_KEY]")
}
