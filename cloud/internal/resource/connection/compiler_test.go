package connection

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestCompileTypedConnectionsEscapesCredentialsDatabaseAndIPv6(t *testing.T) {
	facts := ConnectionFacts{Host: "2001:db8::7", Port: "5432", Database: "dữ/liệu", Username: "u:s@名", Password: `p,;="' 空`, CredentialAvailable: true}
	postgres, err := CompileConnection("postgres", serviceconfigurationv1.SourcePostgresURI, "", facts)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(postgres.Value)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.Scheme != "postgresql" || parsed.Hostname() != facts.Host || parsed.Port() != facts.Port || parsed.User.Username() != facts.Username || password != facts.Password || parsed.Path != "/"+facts.Database || !strings.Contains(postgres.Value, "d%E1%BB%AF%2Fli%E1%BB%87u") {
		t.Fatalf("PostgreSQL URI did not round-trip: %q parsed=%+v", postgres.Value, parsed)
	}
	if postgres.Sensitivity != resourcev1.ValueSecret {
		t.Fatalf("sensitivity=%s", postgres.Sensitivity)
	}

	npgsql, err := CompileConnection("postgres", serviceconfigurationv1.SourcePostgresNpgsql, "", facts)
	if err != nil {
		t.Fatal(err)
	}
	npgsqlValues, err := parseDelimitedOptions(npgsql.Value, ';')
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"Host": facts.Host, "Port": facts.Port, "Database": facts.Database, "Username": facts.Username, "Password": facts.Password, "SSL Mode": "Disable"} {
		if npgsqlValues[key] != want {
			t.Fatalf("Npgsql %s=%q want %q in %q", key, npgsqlValues[key], want, npgsql.Value)
		}
	}

	jdbc, err := CompileConnection("postgres", serviceconfigurationv1.SourcePostgresJDBC, "", facts)
	if err != nil {
		t.Fatal(err)
	}
	jdbcURL, err := url.Parse(strings.TrimPrefix(jdbc.Value, "jdbc:"))
	if err != nil || jdbcURL.Hostname() != facts.Host || jdbcURL.Path != "/"+facts.Database || jdbc.Sensitivity != resourcev1.ValueNonSecret {
		t.Fatalf("JDBC=%q parsed=%+v err=%v", jdbc.Value, jdbcURL, err)
	}
}

func TestCompileRedisStackExchangeURIAndDatabaseIndex(t *testing.T) {
	facts := ConnectionFacts{Host: "cache.internal", Port: "6379", Database: "12", Username: "opsi=名", Password: `p;="ass`, CredentialAvailable: true}
	stack, err := CompileConnection("redis", serviceconfigurationv1.SourceRedisStackExchange, "", facts)
	if err != nil {
		t.Fatal(err)
	}
	options, err := parseStackExchangeOptions(stack.Value)
	if err != nil {
		t.Fatal(err)
	}
	if options["endpoint"] != "cache.internal:6379" || options["user"] != facts.Username || options["password"] != facts.Password || options["defaultDatabase"] != facts.Database {
		t.Fatalf("StackExchange options=%+v raw=%q", options, stack.Value)
	}
	redis, err := CompileConnection("redis", serviceconfigurationv1.SourceRedisURI, "", facts)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(redis.Value)
	password, _ := parsed.User.Password()
	if parsed.Scheme != "redis" || parsed.User.Username() != facts.Username || password != facts.Password || parsed.Path != "/12" {
		t.Fatalf("Redis URI=%q", redis.Value)
	}

	emptyPassword := facts
	emptyPassword.Password = ""
	if _, err := CompileConnection("redis", serviceconfigurationv1.SourceRedisStackExchange, "", emptyPassword); err != nil {
		t.Fatalf("empty password must remain representable: %v", err)
	}
	unrepresentable := facts
	unrepresentable.Password = "comma,password"
	if _, err := CompileConnection("redis", serviceconfigurationv1.SourceRedisStackExchange, "", unrepresentable); err == nil {
		t.Fatal("unrepresentable comma credential accepted")
	}
}

func TestCompilePDOAndNATS(t *testing.T) {
	pdo, err := CompileConnection("postgres", serviceconfigurationv1.SourcePostgresPDODSN, "", ConnectionFacts{Host: "db.internal", Port: "5432", Database: "app"})
	if err != nil || pdo.Value != "pgsql:host=db.internal;port=5432;dbname=app" || pdo.Sensitivity != resourcev1.ValueNonSecret {
		t.Fatalf("PDO=%+v err=%v", pdo, err)
	}
	nats, err := CompileConnection("nats", serviceconfigurationv1.SourceNATSURI, "", ConnectionFacts{Host: "2001:db8::8", Port: "4222"})
	if err != nil || nats.Value != "nats://[2001:db8::8]:4222" || nats.Sensitivity != resourcev1.ValueNonSecret {
		t.Fatalf("NATS=%+v err=%v", nats, err)
	}
	if _, err := CompileConnection("postgres", serviceconfigurationv1.SourcePostgresPDODSN, "", ConnectionFacts{Host: "db;host", Port: "5432", Database: "app"}); err == nil {
		t.Fatal("unsafe PDO host accepted")
	}
}

func TestLegacyURIAndSafeTemplateCompatibility(t *testing.T) {
	facts := ConnectionFacts{Host: "db.internal", Port: "5432", Database: "app", Username: "user@x", Password: "p a:ss", CredentialAvailable: true}
	legacy, err := CompileConnection("postgres", serviceconfigurationv1.SourceConnectionURL, "", facts)
	if err != nil || !strings.HasPrefix(legacy.Value, "postgres://") {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	template := `postgres://{{username|url_userinfo}}:{{password|url_userinfo}}@{{host}}:{{port}}/{{database}}`
	compiled, err := CompileConnection("postgres", serviceconfigurationv1.SourceConnectionTemplate, template, facts)
	if err != nil || !strings.Contains(compiled.Value, "user%40x:p%20a%3Ass@") || compiled.Sensitivity != resourcev1.ValueSecret {
		t.Fatalf("template=%+v err=%v", compiled, err)
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, `password=plaintext`); err == nil {
		t.Fatal("literal credential accepted")
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, `username=admin`); err == nil {
		t.Fatal("literal username accepted")
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, `postgres://admin:literal@{{host}}:{{port}}/{{database}}`); err == nil {
		t.Fatal("literal URL userinfo accepted")
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, `{{password}}`); err == nil {
		t.Fatal("unencoded credential accepted")
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, `${DATABASE_URL}`); err == nil {
		t.Fatal("environment substitution accepted")
	}
	if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, strings.Repeat("x", MaxConnectionTemplateBytes+1)); err == nil {
		t.Fatal("oversized template accepted")
	}
}

func TestCompileFailsClosedForProtocolAndMissingFacts(t *testing.T) {
	cases := map[string]struct {
		protocol, source, template string
		facts                      ConnectionFacts
	}{
		"wrong protocol":     {"redis", serviceconfigurationv1.SourcePostgresNpgsql, "", ConnectionFacts{Host: "h", Port: "6379"}},
		"missing database":   {"postgres", serviceconfigurationv1.SourcePostgresJDBC, "", ConnectionFacts{Host: "h", Port: "5432"}},
		"bad port":           {"nats", serviceconfigurationv1.SourceNATSURI, "", ConnectionFacts{Host: "h", Port: "70000"}},
		"missing credential": {"postgres", serviceconfigurationv1.SourcePostgresNpgsql, "", ConnectionFacts{Host: "h", Port: "5432", Database: "d"}},
	}
	for name, testCase := range cases {
		if _, err := CompileConnection(testCase.protocol, testCase.source, testCase.template, testCase.facts); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestTemplateCredentialValidationUsesParsedTokens(t *testing.T) {
	valid := []string{
		"PASSWORD \t = \t{{password|kv_quote}};Host={{host}}",
		"User : {{username|url_query}}&Password:{{password|url_query}}",
		"postgres://{{username|url_userinfo}}:{{password|url_userinfo}}@{{host}}:{{port}}/{{database}}",
		"redis://:{{password|url_userinfo}}@{{host}}:{{port}}",
	}
	for _, template := range valid {
		if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, template); err != nil {
			t.Fatalf("valid parsed credential template rejected: %v", err)
		}
	}
	invalid := []string{
		"password={{host}}",
		"password=prefix{{password|kv_quote}}",
		"password={{password|kv_quote}}suffix",
		"password={{password|kv_quote}} suffix",
		"password=literal;Password={{password|kv_quote}}",
		"postgres://admin:{{password|url_userinfo}}@{{host}}:{{port}}",
		"postgres://{{username|url_userinfo}}:literal@{{host}}:{{port}}",
		"{{password|url_query|kv_quote}}",
		"{{host {{port}} }}",
	}
	for _, template := range invalid {
		if _, err := LookupSource("postgres", serviceconfigurationv1.SourceConnectionTemplate, template); err == nil {
			t.Fatalf("unsafe parsed credential template accepted")
		}
	}
}

func TestCompileErrorCodesAreStableAndDoNotExposeFacts(t *testing.T) {
	secretFact := "credential-do-not-leak"
	cases := map[string]struct {
		want string
		err  error
	}{
		"protocol":           {ErrorUnsupportedProtocol, compileErrorFrom("kafka", serviceconfigurationv1.SourceConnectionTemplate, "password="+secretFact, ConnectionFacts{})},
		"source":             {ErrorUnsupportedSource, compileErrorFrom("postgres", serviceconfigurationv1.SourceRedisURI, "", ConnectionFacts{})},
		"template":           {ErrorInvalidTemplate, compileErrorFrom("postgres", serviceconfigurationv1.SourceConnectionTemplate, "password="+secretFact, ConnectionFacts{})},
		"fact":               {ErrorInvalidFact, compileErrorFrom("nats", serviceconfigurationv1.SourceNATSURI, "", ConnectionFacts{Host: secretFact + "@", Port: "4222"})},
		"missing fact":       {ErrorMissingFact, compileErrorFrom("postgres", serviceconfigurationv1.SourcePostgresJDBC, "", ConnectionFacts{Host: "host", Port: "5432"})},
		"missing credential": {ErrorMissingCredential, compileErrorFrom("postgres", serviceconfigurationv1.SourcePostgresNpgsql, "", ConnectionFacts{Host: "host", Port: "5432", Database: "db"})},
		"unrepresentable":    {ErrorUnrepresentableValue, compileErrorFrom("redis", serviceconfigurationv1.SourceRedisStackExchange, "", ConnectionFacts{Host: "host", Port: "6379", Username: "user", Password: secretFact + ",bad", CredentialAvailable: true})},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if testCase.err == nil {
				t.Fatal("expected compile error")
			}
			var compileErr CompileError
			if !errors.As(testCase.err, &compileErr) || compileErr.Code() != testCase.want {
				t.Fatalf("error=%v code=%q want=%q", testCase.err, compileErr.Code(), testCase.want)
			}
			if strings.Contains(testCase.err.Error(), secretFact) {
				t.Fatalf("compile error leaked a fact")
			}
		})
	}
}

func TestTemplateCompilationPreservesBytesAroundPlaceholders(t *testing.T) {
	facts := ConnectionFacts{Host: "db.internal", Port: "5432", Database: "app"}
	first := " host={{host}}\nport={{port}} "
	second := "host={{host}}\nport={{port}}"
	compiledFirst, err := CompileConnection("postgres", serviceconfigurationv1.SourceConnectionTemplate, first, facts)
	if err != nil {
		t.Fatal(err)
	}
	compiledSecond, err := CompileConnection("postgres", serviceconfigurationv1.SourceConnectionTemplate, second, facts)
	if err != nil {
		t.Fatal(err)
	}
	if compiledFirst.Value != " host=db.internal\nport=5432 " || compiledSecond.Value != "host=db.internal\nport=5432" || compiledFirst.Value == compiledSecond.Value {
		t.Fatalf("template bytes were not preserved: first=%q second=%q", compiledFirst.Value, compiledSecond.Value)
	}
}

func compileErrorFrom(protocol, source, template string, facts ConnectionFacts) error {
	_, err := CompileConnection(protocol, source, template, facts)
	return err
}

func TestSelectBindingRequiresCompleteIdentityAndRejectsAmbiguity(t *testing.T) {
	identity := BindingIdentity{SourceServiceID: "app-1", TargetResourceID: "res-1", LogicalName: "database", Protocol: "postgres", Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: "binding-1"}
	valid := resourcev1.Binding{ID: "binding-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"}, Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-1"}, LogicalName: "database", Protocol: resourcev1.ProtocolPostgres, Lifecycle: resourcev1.LifecycleReady}
	if selected, ok := SelectBinding([]resourcev1.Binding{valid}, identity); !ok || selected.ID != valid.ID {
		t.Fatalf("exact binding was not selected: %+v ok=%t", selected, ok)
	}
	mutations := []func(*resourcev1.Binding){
		func(value *resourcev1.Binding) { value.Source.ID = "app-2" },
		func(value *resourcev1.Binding) { value.Target.ID = "res-2" },
		func(value *resourcev1.Binding) { value.LogicalName = "other" },
		func(value *resourcev1.Binding) { value.Protocol = resourcev1.ProtocolRedis },
		func(value *resourcev1.Binding) { value.Lifecycle = resourcev1.LifecycleDeleting },
		func(value *resourcev1.Binding) { value.ID = "binding-stale" },
	}
	for _, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if _, ok := SelectBinding([]resourcev1.Binding{candidate}, identity); ok {
			t.Fatalf("mismatched binding selected: %+v", candidate)
		}
	}
	withoutSelection := identity
	withoutSelection.SelectedBindingID = ""
	duplicate := valid
	duplicate.ID = "binding-2"
	if _, ok := SelectBinding([]resourcev1.Binding{valid, duplicate}, withoutSelection); ok {
		t.Fatal("ambiguous bindings with the same logical name were selected")
	}
}

func parseDelimitedOptions(value string, delimiter byte) (map[string]string, error) {
	parts := []string{}
	var part strings.Builder
	quoted := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '"' {
			if quoted && index+1 < len(value) && value[index+1] == '"' {
				part.WriteByte('"')
				index++
				continue
			}
			quoted = !quoted
			continue
		}
		if character == delimiter && !quoted {
			parts = append(parts, part.String())
			part.Reset()
			continue
		}
		part.WriteByte(character)
	}
	if quoted {
		return nil, &url.Error{Op: "parse", URL: value, Err: errUnclosedQuote{}}
	}
	parts = append(parts, part.String())
	result := map[string]string{}
	for index, raw := range parts {
		key, value, found := strings.Cut(raw, "=")
		if !found {
			if index == 0 && delimiter == ',' {
				result["endpoint"] = raw
				continue
			}
			return nil, &url.Error{Op: "parse", URL: raw, Err: errMissingEquals{}}
		}
		result[key] = value
	}
	return result, nil
}

func parseStackExchangeOptions(value string) (map[string]string, error) {
	parts := strings.Split(value, ",")
	result := map[string]string{"endpoint": parts[0]}
	for _, raw := range parts[1:] {
		key, option, found := strings.Cut(raw, "=")
		if !found {
			return nil, &url.Error{Op: "parse", URL: raw, Err: errMissingEquals{}}
		}
		result[key] = option
	}
	return result, nil
}

type errUnclosedQuote struct{}

func (errUnclosedQuote) Error() string { return "unclosed quote" }

type errMissingEquals struct{}

func (errMissingEquals) Error() string { return "missing equals" }
