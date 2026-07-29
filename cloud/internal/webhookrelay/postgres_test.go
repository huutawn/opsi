package webhookrelay

import (
	"os"
	"testing"
)

func requirePostgresTestDSN(t *testing.T, name string) string {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		msg := "set OPSI_TEST_DATABASE_URL to run Postgres " + name + " test"
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return dsn
}
