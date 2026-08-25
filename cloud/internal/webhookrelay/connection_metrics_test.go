package webhookrelay

import (
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
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
