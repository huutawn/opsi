package repositoryanalysis

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type connectionEvidence struct {
	Protocol        string
	EnvironmentName string
	SymbolicSource  string
	Reason          string
}

var (
	aspNetEnvironmentKey = regexp.MustCompile(`ConnectionStrings__([^\s"'` + "`" + `:;,)}\]]*)`)
	aspNetColonKey       = regexp.MustCompile(`ConnectionStrings\s*:\s*([^\s"'` + "`" + `:;,)}\]]*)`)
	aspNetLiteralGetter  = regexp.MustCompile(`GetConnectionString\s*\(\s*"([^"]*)"\s*\)`)
	aspNetAnyGetter      = regexp.MustCompile(`GetConnectionString\s*\(`)
	aspNetKeyName        = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func detectConnectionEvidence(filePath, text string) ([]connectionEvidence, bool) {
	base := strings.ToLower(path.Base(filePath))
	if strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".rst") || strings.HasSuffix(base, ".txt") {
		return nil, false
	}
	values := []connectionEvidence{}
	add := func(protocol, environment, source, reason string) {
		for _, value := range values {
			if value.Protocol == protocol && value.EnvironmentName == environment {
				return
			}
		}
		values = append(values, connectionEvidence{Protocol: protocol, EnvironmentName: environment, SymbolicSource: source, Reason: reason})
	}
	containsAny := func(markers ...string) bool {
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				return true
			}
		}
		return false
	}

	aspNetAmbiguous := detectASPNetConnectionStrings(text, add)
	if containsAny("SignalR__Redis__ConnectionString", "SignalR:Redis:ConnectionString") || strings.Contains(text, "SignalR") && strings.Contains(text, "Redis") && strings.Contains(text, "ConnectionString") {
		add("redis", "SignalR__Redis__ConnectionString", serviceconfigurationv1.SourceRedisStackExchange, "ASP.NET SignalR configuration requires a StackExchange.Redis connection string.")
	}
	if containsAny("SPRING_DATASOURCE_URL", "spring.datasource.url", "jdbc:postgresql:") {
		add("postgres", "SPRING_DATASOURCE_URL", serviceconfigurationv1.SourcePostgresJDBC, "Spring datasource configuration requires a JDBC PostgreSQL URL.")
		add("postgres", "SPRING_DATASOURCE_USERNAME", serviceconfigurationv1.SourceCredentialUsername, "Spring datasource credentials are configured separately.")
		add("postgres", "SPRING_DATASOURCE_PASSWORD", serviceconfigurationv1.SourceCredentialPassword, "Spring datasource credentials are configured separately.")
	}
	lower := strings.ToLower(text)
	if (strings.Contains(lower, "db_connection") && (strings.Contains(lower, "pgsql") || strings.Contains(lower, "postgres"))) || strings.Contains(lower, "pdo_pgsql") {
		for environment, source := range map[string]string{
			"DB_HOST": serviceconfigurationv1.SourceResourceHost, "DB_PORT": serviceconfigurationv1.SourceResourcePort,
			"DB_DATABASE": serviceconfigurationv1.SourceCredentialDatabase, "DB_USERNAME": serviceconfigurationv1.SourceCredentialUsername,
			"DB_PASSWORD": serviceconfigurationv1.SourceCredentialPassword,
		} {
			add("postgres", environment, source, "Laravel/PDO PostgreSQL configuration declares an exact atomic key.")
		}
	}
	for _, environment := range []string{"DB_DSN", "PDO_DSN"} {
		if containsIdentifier(text, environment) {
			add("postgres", environment, serviceconfigurationv1.SourcePostgresPDODSN, "PDO configuration declares an exact PostgreSQL DSN key.")
		}
	}
	for _, environment := range []string{"DATABASE_URL", "POSTGRES_URL", "POSTGRESQL_URL", "PG_URL"} {
		if containsIdentifier(text, environment) {
			add("postgres", environment, serviceconfigurationv1.SourcePostgresURI, "Repository configuration declares an exact PostgreSQL URI key.")
		}
	}
	for environment, source := range map[string]string{
		"PGHOST": serviceconfigurationv1.SourceResourceHost, "PGPORT": serviceconfigurationv1.SourceResourcePort,
		"PGDATABASE": serviceconfigurationv1.SourceCredentialDatabase, "PGUSER": serviceconfigurationv1.SourceCredentialUsername,
		"PGPASSWORD": serviceconfigurationv1.SourceCredentialPassword,
	} {
		if containsIdentifier(text, environment) {
			add("postgres", environment, source, "Repository configuration declares an exact libpq environment key.")
		}
	}
	for _, environment := range []string{"REDIS_URL", "VALKEY_URL"} {
		if containsIdentifier(text, environment) {
			add("redis", environment, serviceconfigurationv1.SourceRedisURI, "Repository configuration declares an exact Redis/Valkey URI key.")
		}
	}
	for environment, source := range map[string]string{
		"REDIS_HOST": serviceconfigurationv1.SourceResourceHost, "REDIS_PORT": serviceconfigurationv1.SourceResourcePort,
		"REDIS_USERNAME": serviceconfigurationv1.SourceCredentialUsername, "REDIS_PASSWORD": serviceconfigurationv1.SourceCredentialPassword,
		"CACHE_HOST": serviceconfigurationv1.SourceResourceHost, "CACHE_PORT": serviceconfigurationv1.SourceResourcePort,
		"CACHE_USERNAME": serviceconfigurationv1.SourceCredentialUsername, "CACHE_PASSWORD": serviceconfigurationv1.SourceCredentialPassword,
	} {
		if containsIdentifier(text, environment) {
			add("redis", environment, source, "Repository configuration declares an exact Redis/Valkey atomic key.")
		}
	}
	for _, environment := range []string{"NATS_URL", "NATS_URI"} {
		if containsIdentifier(text, environment) {
			add("nats", environment, serviceconfigurationv1.SourceNATSURI, "Repository configuration declares an exact NATS URI key.")
		}
	}
	for environment, source := range map[string]string{"NATS_HOST": serviceconfigurationv1.SourceResourceHost, "NATS_PORT": serviceconfigurationv1.SourceResourcePort} {
		if containsIdentifier(text, environment) {
			add("nats", environment, source, "Repository configuration declares an exact NATS atomic key.")
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Protocol+"\x00"+values[i].EnvironmentName < values[j].Protocol+"\x00"+values[j].EnvironmentName
	})
	ambiguous := aspNetAmbiguous
	for _, marker := range []string{"DB_CONNECTION_STRING", "DATABASE_CONNECTION_STRING", "CONNECTION_STRING"} {
		if strings.Contains(text, marker) && !strings.Contains(text, "ConnectionStrings__") && !strings.Contains(text, "SignalR__Redis__") {
			ambiguous = true
		}
	}
	return values, ambiguous
}

func detectASPNetConnectionStrings(text string, add func(protocol, environment, source, reason string)) bool {
	ambiguous := false
	addKey := func(name string) {
		if !aspNetKeyName.MatchString(name) || len("ConnectionStrings__"+name) > 128 {
			ambiguous = true
			return
		}
		add("postgres", "ConnectionStrings__"+name, serviceconfigurationv1.SourcePostgresNpgsql, "ASP.NET configuration declares an exact Npgsql connection string key.")
	}
	for _, match := range aspNetEnvironmentKey.FindAllStringSubmatch(text, -1) {
		addKey(match[1])
	}
	for _, match := range aspNetColonKey.FindAllStringSubmatch(text, -1) {
		addKey(match[1])
	}
	getterFree := aspNetLiteralGetter.ReplaceAllStringFunc(text, func(value string) string {
		match := aspNetLiteralGetter.FindStringSubmatch(value)
		addKey(match[1])
		return ""
	})
	if aspNetAnyGetter.MatchString(getterFree) {
		ambiguous = true
	}
	var document map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &document) == nil {
		if raw, ok := document["ConnectionStrings"]; ok {
			var values map[string]json.RawMessage
			if json.Unmarshal(raw, &values) != nil {
				ambiguous = true
			} else {
				for name := range values {
					addKey(name)
				}
			}
		}
	} else if strings.Contains(text, `"ConnectionStrings"`) {
		ambiguous = true
	}
	return ambiguous
}

func containsIdentifier(text, identifier string) bool {
	for offset := 0; offset <= len(text)-len(identifier); {
		index := strings.Index(text[offset:], identifier)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !identifierCharacter(text[index-1])
		after := index + len(identifier)
		afterOK := after == len(text) || !identifierCharacter(text[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + 1
	}
	return false
}

func identifierCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func sourceForEnvironment(protocol, environment string) (string, bool) {
	text := environment
	if environment == "SignalR__Redis__ConnectionString" {
		text += " SignalR Redis ConnectionString"
	}
	values, _ := detectConnectionEvidence("environment", text)
	for _, value := range values {
		if value.Protocol == protocol && value.EnvironmentName == environment {
			return value.SymbolicSource, true
		}
	}
	return "", false
}
