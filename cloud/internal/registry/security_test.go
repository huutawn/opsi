package registry

import (
	"strings"
	"testing"
)

func TestRedactStringAndAuditMetadata(t *testing.T) {
	secret := "password=hunter2 token=abc K3S_TOKEN=join DATABASE_URL=postgres://u:p@h/db Authorization: Bearer abc\n-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	redacted := RedactString(secret)
	for _, leaked := range []string{"hunter2", "token=abc", "K3S_TOKEN=join", "postgres://", "Bearer abc", "OPENSSH PRIVATE KEY"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redaction leaked %q in %q", leaked, redacted)
		}
	}

	service := NewService()
	service.Audit("org", "proj", "user", "TEST", "thing", "id", "success", map[string]any{"raw": secret})
	if got := service.audit[0].MetadataRedacted["raw"].(string); strings.Contains(got, "hunter2") {
		t.Fatalf("audit metadata leaked secret: %q", got)
	}
}
