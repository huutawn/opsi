package webhookrelay

import (
	"fmt"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestRecordConnectionAnalysisMetricsUsesOnlyDialectAndAmbiguity(t *testing.T) {
	server := &Server{observer: NewObserver()}
	server.recordConnectionAnalysisMetrics(repositoryanalysis.Result{
		Dependencies: []repositoryanalysis.Dependency{{
			Protocol: "postgres",
			Injections: []repositoryanalysis.Injection{{
				EnvironmentName: "DATABASE_URL",
				SymbolicSource:  "connection.postgres.npgsql",
			}},
		}},
		Issues: []repositoryanalysis.Issue{{Code: "CONNECTION_DIALECT_REQUIRED", Blocking: true}},
	})

	counters := server.observer.Snapshot().Counters
	if counters["connection_dialect_postgres_connection_postgres_npgsql_total"] != 1 {
		t.Fatalf("dialect counter=%v", counters)
	}
	if counters["connection_dialect_ambiguity_total"] != 1 {
		t.Fatalf("ambiguity counter=%v", counters)
	}
	for name := range counters {
		if name == "DATABASE_URL" {
			t.Fatalf("metric exposed environment name: %v", counters)
		}
	}
}

func TestConnectionCompileMetricsUseOnlyStableErrorCodes(t *testing.T) {
	server := &Server{observer: NewObserver()}
	compile := func(protocol, source, template string, facts resourcecompiler.ConnectionFacts) error {
		_, err := resourcecompiler.CompileConnection(protocol, source, template, facts)
		return fmt.Errorf("runtime compilation: %w", err)
	}
	errorsByCode := map[string]error{
		resourcecompiler.ErrorUnsupportedProtocol:  compile("kafka", serviceconfigurationv1.SourceConnectionTemplate, "password=metric-secret", resourcecompiler.ConnectionFacts{}),
		resourcecompiler.ErrorUnsupportedSource:    compile("postgres", serviceconfigurationv1.SourceRedisURI, "", resourcecompiler.ConnectionFacts{}),
		resourcecompiler.ErrorInvalidTemplate:      compile("postgres", serviceconfigurationv1.SourceConnectionTemplate, "password=metric-secret", resourcecompiler.ConnectionFacts{}),
		resourcecompiler.ErrorInvalidFact:          compile("nats", serviceconfigurationv1.SourceNATSURI, "", resourcecompiler.ConnectionFacts{Host: "bad@metric-secret", Port: "4222"}),
		resourcecompiler.ErrorMissingFact:          compile("postgres", serviceconfigurationv1.SourcePostgresJDBC, "", resourcecompiler.ConnectionFacts{Host: "host", Port: "5432"}),
		resourcecompiler.ErrorMissingCredential:    compile("postgres", serviceconfigurationv1.SourcePostgresNpgsql, "", resourcecompiler.ConnectionFacts{Host: "host", Port: "5432", Database: "db"}),
		resourcecompiler.ErrorUnrepresentableValue: compile("redis", serviceconfigurationv1.SourceRedisStackExchange, "", resourcecompiler.ConnectionFacts{Host: "host", Port: "6379", Username: "user", Password: "metric-secret,bad", CredentialAvailable: true}),
	}
	for code, err := range errorsByCode {
		server.observeConnectionCompileError(err)
		if got := server.observer.Snapshot().Counters["connection_compile_error_"+code+"_total"]; got != 1 {
			t.Fatalf("metric code %s=%d", code, got)
		}
	}
	metrics := server.observer.Snapshot().Prometheus()
	for _, forbidden := range []string{"metric-secret", "password=", "DATABASE_URL", "connection.template"} {
		if strings.Contains(metrics, forbidden) {
			t.Fatalf("metrics exposed sensitive compile context %q", forbidden)
		}
	}
}
