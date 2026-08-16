package resourcev1

import "testing"

func TestParsePostgresVersion(t *testing.T) {
	for _, input := range []string{"18.6", "18.6 (Debian 18.6-1.pgdg12+2)"} {
		version, ok := ParsePostgresVersion(input)
		if !ok || version.Major != 18 || version.Minor != 6 || version.String() != "18.6" {
			t.Fatalf("ParsePostgresVersion(%q) = %+v, %t", input, version, ok)
		}
	}
}

func TestParsePostgresVersionRejectsMalformedValues(t *testing.T) {
	for _, input := range []string{"", "unknown", "18", "18.06", "PostgreSQL 18.6", "18.6 trailing", "18.6 (Debian", "18.6)", "18.6 (Debian\n18.6)"} {
		if version, ok := ParsePostgresVersion(input); ok {
			t.Fatalf("ParsePostgresVersion(%q) = %+v, true", input, version)
		}
	}
}

func TestParsePostgresToolRelease(t *testing.T) {
	for _, input := range []string{
		"pg_restore (PostgreSQL) 18.6",
		"pg_restore (PostgreSQL) 18.6 (Debian 18.6-1.pgdg12+2)",
	} {
		version, ok := ParsePostgresToolRelease(input)
		if !ok || version.Major != 18 || version.Minor != 6 || version.String() != "18.6" {
			t.Fatalf("ParsePostgresToolRelease(%q) = %+v, %t", input, version, ok)
		}
	}
}

func TestParsePostgresToolReleaseRejectsMalformedValues(t *testing.T) {
	for _, input := range []string{
		"",
		"unknown",
		"pg_restore (PostgreSQL) ",
		"pg_restore 18.6",
		"18.6",
		"pg_restore wrapper 18.6 fake",
		"pg_restore (PostgreSQL) 18.6 trailing",
		"arbitrary text 18.6",
		"pg_restore (PostgreSQL) 18.6\n18.6",
	} {
		if version, ok := ParsePostgresToolRelease(input); ok {
			t.Fatalf("ParsePostgresToolRelease(%q) = %+v, true", input, version)
		}
	}
}

func TestCompatiblePostgresVersions(t *testing.T) {
	for _, test := range []struct {
		source, target string
		want           bool
	}{
		{"18.6 (Debian 18.6-1.pgdg12+2)", "18.6", true},
		{"18.6", "18.6", true},
		{"18.5", "18.6", false},
		{"17.6", "18.6", false},
		{"19.0", "18.6", false},
		{"unknown", "18.6", false},
		{"18.6", "unknown", false},
		{"", "18.6", false},
		{"18.6", "", false},
	} {
		if got := CompatiblePostgresVersions(test.source, test.target); got != test.want {
			t.Fatalf("CompatiblePostgresVersions(%q, %q) = %t, want %t", test.source, test.target, got, test.want)
		}
	}
}
