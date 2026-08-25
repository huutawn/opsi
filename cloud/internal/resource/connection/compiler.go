package connection

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

const MaxConnectionTemplateBytes = 1024

type SourceDescriptor struct {
	Source        string
	Protocol      string
	Sensitivity   resourcev1.ValueSensitivity
	Deprecated    bool
	RequiredFacts []string
	compile       sourceCompiler
}

type sourceCompiler func(ConnectionFacts) (string, error)

type ConnectionFacts struct {
	Host                string
	Port                string
	Database            string
	Username            string
	Password            string
	CredentialAvailable bool
}

type CompiledConnection struct {
	Value       string
	Sensitivity resourcev1.ValueSensitivity
}

var sourceCatalog = map[string]SourceDescriptor{
	"postgres\x00" + serviceconfigurationv1.SourceResourceHost:       descriptor("postgres", serviceconfigurationv1.SourceResourceHost, resourcev1.ValueNonSecret, factCompiler("host"), "host"),
	"postgres\x00" + serviceconfigurationv1.SourceResourcePort:       descriptor("postgres", serviceconfigurationv1.SourceResourcePort, resourcev1.ValueNonSecret, factCompiler("port"), "port"),
	"postgres\x00" + serviceconfigurationv1.SourceCredentialDatabase: descriptor("postgres", serviceconfigurationv1.SourceCredentialDatabase, resourcev1.ValueNonSecret, factCompiler("database"), "database"),
	"postgres\x00" + serviceconfigurationv1.SourceCredentialUsername: descriptor("postgres", serviceconfigurationv1.SourceCredentialUsername, resourcev1.ValueSecret, factCompiler("username"), "credential"),
	"postgres\x00" + serviceconfigurationv1.SourceCredentialPassword: descriptor("postgres", serviceconfigurationv1.SourceCredentialPassword, resourcev1.ValueSecret, factCompiler("password"), "credential"),
	"postgres\x00" + serviceconfigurationv1.SourcePostgresURI:        descriptor("postgres", serviceconfigurationv1.SourcePostgresURI, resourcev1.ValueSecret, compilePostgresURI, "host", "port", "database", "credential"),
	"postgres\x00" + serviceconfigurationv1.SourcePostgresNpgsql:     descriptor("postgres", serviceconfigurationv1.SourcePostgresNpgsql, resourcev1.ValueSecret, compileNpgsql, "host", "port", "database", "credential"),
	"postgres\x00" + serviceconfigurationv1.SourcePostgresJDBC:       descriptor("postgres", serviceconfigurationv1.SourcePostgresJDBC, resourcev1.ValueNonSecret, compileJDBC, "host", "port", "database"),
	"postgres\x00" + serviceconfigurationv1.SourcePostgresPDODSN:     descriptor("postgres", serviceconfigurationv1.SourcePostgresPDODSN, resourcev1.ValueNonSecret, compilePDODSN, "host", "port", "database"),
	"redis\x00" + serviceconfigurationv1.SourceResourceHost:          descriptor("redis", serviceconfigurationv1.SourceResourceHost, resourcev1.ValueNonSecret, factCompiler("host"), "host"),
	"redis\x00" + serviceconfigurationv1.SourceResourcePort:          descriptor("redis", serviceconfigurationv1.SourceResourcePort, resourcev1.ValueNonSecret, factCompiler("port"), "port"),
	"redis\x00" + serviceconfigurationv1.SourceCredentialDatabase:    descriptor("redis", serviceconfigurationv1.SourceCredentialDatabase, resourcev1.ValueNonSecret, factCompiler("database"), "database"),
	"redis\x00" + serviceconfigurationv1.SourceCredentialUsername:    descriptor("redis", serviceconfigurationv1.SourceCredentialUsername, resourcev1.ValueSecret, factCompiler("username"), "credential"),
	"redis\x00" + serviceconfigurationv1.SourceCredentialPassword:    descriptor("redis", serviceconfigurationv1.SourceCredentialPassword, resourcev1.ValueSecret, factCompiler("password"), "credential"),
	"redis\x00" + serviceconfigurationv1.SourceRedisURI:              descriptor("redis", serviceconfigurationv1.SourceRedisURI, resourcev1.ValueSecret, compileRedisURI, "host", "port", "credential"),
	"redis\x00" + serviceconfigurationv1.SourceRedisStackExchange:    descriptor("redis", serviceconfigurationv1.SourceRedisStackExchange, resourcev1.ValueSecret, compileStackExchange, "host", "port", "credential"),
	"nats\x00" + serviceconfigurationv1.SourceResourceHost:           descriptor("nats", serviceconfigurationv1.SourceResourceHost, resourcev1.ValueNonSecret, factCompiler("host"), "host"),
	"nats\x00" + serviceconfigurationv1.SourceResourcePort:           descriptor("nats", serviceconfigurationv1.SourceResourcePort, resourcev1.ValueNonSecret, factCompiler("port"), "port"),
	"nats\x00" + serviceconfigurationv1.SourceNATSURI:                descriptor("nats", serviceconfigurationv1.SourceNATSURI, resourcev1.ValueNonSecret, compileNATSURI, "host", "port"),
}

var applicationSources = map[string]map[string]bool{
	serviceconfigurationv1.StrategyInternalHTTP: {
		serviceconfigurationv1.SourceApplicationInternalURL: true, serviceconfigurationv1.SourceApplicationInternalHost: true,
		serviceconfigurationv1.SourceApplicationInternalPort: true, serviceconfigurationv1.SourceApplicationPath: true,
	},
	serviceconfigurationv1.StrategyPublicHTTP: {
		serviceconfigurationv1.SourceApplicationPublicURL: true, serviceconfigurationv1.SourceApplicationPublicHost: true,
		serviceconfigurationv1.SourceApplicationPublicPort: true, serviceconfigurationv1.SourceApplicationPublicScheme: true,
		serviceconfigurationv1.SourceApplicationPath: true, serviceconfigurationv1.SourceApplicationURL: true,
	},
	serviceconfigurationv1.StrategySameOrigin: {
		serviceconfigurationv1.SourceApplicationPath: true, serviceconfigurationv1.SourceApplicationURL: true,
	},
}

func LookupSource(protocol, source, template string) (SourceDescriptor, error) {
	protocol, source = strings.TrimSpace(protocol), strings.TrimSpace(source)
	if !ValidManagedProtocol(protocol) {
		return SourceDescriptor{}, compileError(ErrorUnsupportedProtocol, "managed connection protocol is unsupported")
	}
	if source == serviceconfigurationv1.SourceConnectionTemplate {
		sensitive, err := validateConnectionTemplate(template)
		if err != nil {
			return SourceDescriptor{}, err
		}
		if protocol == serviceconfigurationv1.ProtocolNATS && sensitive {
			return SourceDescriptor{}, compileError(ErrorInvalidTemplate, "managed NATS templates cannot reference credentials")
		}
		return SourceDescriptor{Source: source, Protocol: protocol, Sensitivity: sensitivity(sensitive), compile: func(facts ConnectionFacts) (string, error) {
			return executeConnectionTemplate(template, facts)
		}}, nil
	}
	if descriptor, ok := sourceCatalog[protocol+"\x00"+source]; ok {
		if template != "" {
			return SourceDescriptor{}, compileError(ErrorUnsupportedSource, "template is only allowed for connection.template")
		}
		return descriptor, nil
	}
	if source == serviceconfigurationv1.SourceConnectionURL || legacyConnectionSource(source) {
		canonical := legacyURIForProtocol(protocol)
		if canonical == "" || template != "" {
			return SourceDescriptor{}, compileError(ErrorUnsupportedSource, "symbolic source is invalid for the managed connection protocol")
		}
		descriptor := sourceCatalog[protocol+"\x00"+canonical]
		descriptor.Source, descriptor.Deprecated = source, true
		if protocol == serviceconfigurationv1.ProtocolPostgres {
			descriptor.compile = compileLegacyPostgresURI
		}
		return descriptor, nil
	}
	return SourceDescriptor{}, compileError(ErrorUnsupportedSource, "symbolic source is invalid for the managed connection protocol")
}

func ValidManagedProtocol(protocol string) bool {
	switch strings.TrimSpace(protocol) {
	case serviceconfigurationv1.ProtocolPostgres, serviceconfigurationv1.ProtocolRedis, serviceconfigurationv1.ProtocolNATS:
		return true
	default:
		return false
	}
}

func ValidApplicationSource(strategy, source, template string) bool {
	return template == "" && applicationSources[strings.TrimSpace(strategy)][strings.TrimSpace(source)]
}

func CanonicalSource(protocol, source string) string {
	if source == serviceconfigurationv1.SourceConnectionURL || legacyConnectionSource(source) {
		if canonical := legacyURIForProtocol(protocol); canonical != "" {
			return canonical
		}
	}
	return source
}

func CompileConnection(protocol, source, template string, facts ConnectionFacts) (CompiledConnection, error) {
	descriptor, err := LookupSource(protocol, source, template)
	if err != nil {
		return CompiledConnection{}, err
	}
	if err := validateFacts(facts); err != nil {
		return CompiledConnection{}, err
	}
	if err := validateRequiredFacts(descriptor, facts); err != nil {
		return CompiledConnection{}, err
	}
	value, err := descriptor.compile(facts)
	if err != nil {
		return CompiledConnection{}, err
	}
	return CompiledConnection{Value: value, Sensitivity: descriptor.Sensitivity}, nil
}

func compilePostgresURI(facts ConnectionFacts) (string, error) {
	return compilePostgresURIWithScheme("postgresql", facts)
}

func compileLegacyPostgresURI(facts ConnectionFacts) (string, error) {
	return compilePostgresURIWithScheme("postgres", facts)
}

func compilePostgresURIWithScheme(scheme string, facts ConnectionFacts) (string, error) {
	connection := url.URL{Scheme: scheme, User: url.UserPassword(facts.Username, facts.Password), Host: hostPort(facts.Host, facts.Port), Path: "/" + facts.Database, RawPath: "/" + url.PathEscape(facts.Database), RawQuery: "sslmode=disable"}
	return connection.String(), nil
}

func compileRedisURI(facts ConnectionFacts) (string, error) {
	connection := url.URL{Scheme: "redis", User: url.UserPassword(facts.Username, facts.Password), Host: hostPort(facts.Host, facts.Port)}
	if facts.Database != "" {
		connection.Path = "/" + facts.Database
		connection.RawPath = "/" + url.PathEscape(facts.Database)
	}
	return connection.String(), nil
}

func compileNATSURI(facts ConnectionFacts) (string, error) {
	return (&url.URL{Scheme: "nats", Host: hostPort(facts.Host, facts.Port)}).String(), nil
}

func compileNpgsql(facts ConnectionFacts) (string, error) {
	return "Host=" + kvQuote(facts.Host) + ";Port=" + facts.Port + ";Database=" + kvQuote(facts.Database) + ";Username=" + kvQuote(facts.Username) + ";Password=" + kvQuote(facts.Password) + ";SSL Mode=Disable", nil
}

func compileStackExchange(facts ConnectionFacts) (string, error) {
	for _, value := range []string{facts.Username, facts.Password} {
		if strings.Contains(value, ",") || strings.TrimSpace(value) != value {
			return "", compileError(ErrorUnrepresentableValue, "credential cannot be represented safely in a StackExchange.Redis configuration string")
		}
	}
	value := hostPort(facts.Host, facts.Port) + ",user=" + facts.Username + ",password=" + facts.Password
	if facts.Database != "" {
		database, databaseErr := strconv.Atoi(facts.Database)
		if databaseErr != nil || database < 0 {
			return "", compileError(ErrorInvalidFact, "database index is invalid")
		}
		value += ",defaultDatabase=" + facts.Database
	}
	return value, nil
}

func compileJDBC(facts ConnectionFacts) (string, error) {
	return "jdbc:postgresql://" + hostPort(facts.Host, facts.Port) + "/" + url.PathEscape(facts.Database), nil
}

func compilePDODSN(facts ConnectionFacts) (string, error) {
	for _, value := range []string{facts.Host, facts.Database} {
		if strings.ContainsAny(value, ";='") {
			return "", compileError(ErrorUnrepresentableValue, "connection fact cannot be represented safely in a PDO DSN")
		}
	}
	return "pgsql:host=" + facts.Host + ";port=" + facts.Port + ";dbname=" + facts.Database, nil
}

func validateFacts(facts ConnectionFacts) error {
	for _, value := range []string{facts.Host, facts.Port, facts.Database, facts.Username, facts.Password} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return compileError(ErrorInvalidFact, "connection fact contains control characters")
		}
	}
	if facts.Port != "" {
		port, err := strconv.Atoi(facts.Port)
		if err != nil || port < 1 || port > 65535 {
			return compileError(ErrorInvalidFact, "connection port is invalid")
		}
	}
	if strings.ContainsAny(facts.Host, "/?#@") {
		return compileError(ErrorInvalidFact, "connection host is invalid")
	}
	return nil
}

func descriptor(protocol, source string, sensitivity resourcev1.ValueSensitivity, compile sourceCompiler, requiredFacts ...string) SourceDescriptor {
	return SourceDescriptor{Source: source, Protocol: protocol, Sensitivity: sensitivity, RequiredFacts: requiredFacts, compile: compile}
}

func factCompiler(name string) sourceCompiler {
	return func(facts ConnectionFacts) (string, error) {
		switch name {
		case "host":
			return facts.Host, nil
		case "port":
			return facts.Port, nil
		case "database":
			return facts.Database, nil
		case "username":
			return facts.Username, nil
		case "password":
			return facts.Password, nil
		default:
			return "", compileError(ErrorUnsupportedSource, "connection fact compiler is unavailable")
		}
	}
}

func validateRequiredFacts(descriptor SourceDescriptor, facts ConnectionFacts) error {
	values := map[string]string{"host": facts.Host, "port": facts.Port, "database": facts.Database}
	for _, name := range descriptor.RequiredFacts {
		if name == "credential" {
			if !facts.CredentialAvailable {
				return compileError(ErrorMissingCredential, "managed connection credential is unavailable")
			}
			continue
		}
		if values[name] == "" {
			return compileError(ErrorMissingFact, "required connection fact is unavailable")
		}
	}
	return nil
}

func hostPort(host, port string) string {
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return net.JoinHostPort(host, port)
}

func kvQuote(value string) string {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, ";,='\"") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

func sensitivity(secret bool) resourcev1.ValueSensitivity {
	if secret {
		return resourcev1.ValueSecret
	}
	return resourcev1.ValueNonSecret
}

func legacyConnectionSource(source string) bool {
	return strings.HasPrefix(source, "resource.") && strings.HasSuffix(source, ".connection_string") && len(strings.TrimSuffix(strings.TrimPrefix(source, "resource."), ".connection_string")) > 0
}

func legacyURIForProtocol(protocol string) string {
	switch protocol {
	case serviceconfigurationv1.ProtocolPostgres:
		return serviceconfigurationv1.SourcePostgresURI
	case serviceconfigurationv1.ProtocolRedis:
		return serviceconfigurationv1.SourceRedisURI
	case serviceconfigurationv1.ProtocolNATS:
		return serviceconfigurationv1.SourceNATSURI
	default:
		return ""
	}
}
