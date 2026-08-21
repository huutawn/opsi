package resourcev1

import (
	"regexp"
	"strconv"
	"strings"
)

// PostgresRelease is the semantic major.minor PostgreSQL server version.
type PostgresRelease struct {
	Major uint64
	Minor uint64
}

func (v PostgresRelease) String() string {
	return strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10)
}

var postgresVersionPattern = regexp.MustCompile(`^([1-9][0-9]*)\.(0|[1-9][0-9]*)(?: \([^()\r\n]+\))?$`)

// ParsePostgresVersion accepts canonical versions and SHOW server_version provenance.
func ParsePostgresVersion(value string) (PostgresRelease, bool) {
	parts := postgresVersionPattern.FindStringSubmatch(value)
	if parts == nil {
		return PostgresRelease{}, false
	}
	major, majorErr := strconv.ParseUint(parts[1], 10, 64)
	minor, minorErr := strconv.ParseUint(parts[2], 10, 64)
	if majorErr != nil || minorErr != nil {
		return PostgresRelease{}, false
	}
	return PostgresRelease{Major: major, Minor: minor}, true
}

// CompatiblePostgresVersions keeps restore compatibility pinned to the supported release.
func CompatiblePostgresVersions(source, target string) bool {
	sourceRelease, sourceOK := ParsePostgresVersion(source)
	targetRelease, targetOK := ParsePostgresVersion(target)
	supportedRelease, supportedOK := ParsePostgresVersion(PostgresVersion)
	return sourceOK && targetOK && supportedOK && sourceRelease == targetRelease && targetRelease == supportedRelease
}

// ParsePostgresToolRelease extracts and parses the release from pg_restore --version output.
func ParsePostgresToolRelease(value string) (PostgresRelease, bool) {
	prefix := "pg_restore (PostgreSQL) "
	if !strings.HasPrefix(value, prefix) {
		return PostgresRelease{}, false
	}
	return ParsePostgresVersion(value[len(prefix):])
}
