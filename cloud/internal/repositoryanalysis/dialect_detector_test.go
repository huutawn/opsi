package repositoryanalysis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestDialectDetectorCatalogUsesExactEvidence(t *testing.T) {
	tests := map[string]struct {
		path, text string
		want       map[string]string
	}{
		"aspnet":  {"src/appsettings.json", `{"ConnectionStrings":{"Database":""},"SignalR":{"Redis":{"ConnectionString":""}}}`, map[string]string{"ConnectionStrings__Database": serviceconfigurationv1.SourcePostgresNpgsql, "SignalR__Redis__ConnectionString": serviceconfigurationv1.SourceRedisStackExchange}},
		"spring":  {"src/application.properties", "spring.datasource.url=${SPRING_DATASOURCE_URL}\nspring.datasource.username=${SPRING_DATASOURCE_USERNAME}", map[string]string{"SPRING_DATASOURCE_URL": serviceconfigurationv1.SourcePostgresJDBC, "SPRING_DATASOURCE_USERNAME": serviceconfigurationv1.SourceCredentialUsername, "SPRING_DATASOURCE_PASSWORD": serviceconfigurationv1.SourceCredentialPassword}},
		"laravel": {"config/database.php", `'driver' => env('DB_CONNECTION', 'pgsql'), 'host' => env('DB_HOST'), 'password' => env('DB_PASSWORD')`, map[string]string{"DB_HOST": serviceconfigurationv1.SourceResourceHost, "DB_PORT": serviceconfigurationv1.SourceResourcePort, "DB_DATABASE": serviceconfigurationv1.SourceCredentialDatabase, "DB_USERNAME": serviceconfigurationv1.SourceCredentialUsername, "DB_PASSWORD": serviceconfigurationv1.SourceCredentialPassword}},
		"django":  {"settings.py", `DATABASES = dj_database_url.config(default=os.environ["DATABASE_URL"])`, map[string]string{"DATABASE_URL": serviceconfigurationv1.SourcePostgresURI}},
		"rails":   {"config/database.yml", `url: <%= ENV.fetch("DATABASE_URL") %>`, map[string]string{"DATABASE_URL": serviceconfigurationv1.SourcePostgresURI}},
		"node":    {"src/config.ts", `const redis = process.env.REDIS_URL`, map[string]string{"REDIS_URL": serviceconfigurationv1.SourceRedisURI}},
		"go":      {"main.go", `natsURL := os.Getenv("NATS_URL")`, map[string]string{"NATS_URL": serviceconfigurationv1.SourceNATSURI}},
		"rust":    {"src/main.rs", `let host = env::var("PGHOST")?; let pass = env::var("PGPASSWORD")?;`, map[string]string{"PGHOST": serviceconfigurationv1.SourceResourceHost, "PGPASSWORD": serviceconfigurationv1.SourceCredentialPassword}},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			values, ambiguous := detectConnectionEvidence(testCase.path, testCase.text)
			if ambiguous {
				t.Fatal("exact evidence reported as ambiguous")
			}
			got := map[string]string{}
			for _, value := range values {
				got[value.EnvironmentName] = value.SymbolicSource
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("got=%+v want=%+v", got, testCase.want)
			}
		})
	}
}

func TestDialectDetectorDoesNotGuessFromLanguageManifest(t *testing.T) {
	for _, file := range []string{"package.json", "go.mod", "Cargo.toml", "requirements.txt", "Gemfile"} {
		values, ambiguous := detectConnectionEvidence(file, file)
		if len(values) != 0 || ambiguous {
			t.Fatalf("%s produced evidence=%+v ambiguous=%t", file, values, ambiguous)
		}
	}
	values, ambiguous := detectConnectionEvidence("src/config.go", `dsn := os.Getenv("DB_CONNECTION_STRING")`)
	if len(values) != 0 || !ambiguous {
		t.Fatalf("generic connection string values=%+v ambiguous=%t", values, ambiguous)
	}
}

func TestExplicitDialectHasPrecedenceOverFrameworkEvidence(t *testing.T) {
	files := memoryRepository{
		".opsi/opsi-cd.yaml": `version: 2
resources:
  - {logicalName: database, type: postgres, managed: true, required: true}
services:
  - key: api
    build: {context: ., dockerfile: Dockerfile, platform: linux/amd64}
    runtime:
      port: 8080
      dependencies:
        - target: database
          protocol: postgres
          required: true
          injections:
            - {environmentName: DB_DSN, symbolicSource: connection.postgres.pdo_dsn}
`,
		"Dockerfile": "FROM scratch\nEXPOSE 8080",
		"Program.cs": `builder.Configuration.GetConnectionString("Database"); ConnectionStrings__Database`,
	}
	result := analyze(t, files)
	if result.Authority != "explicit_config" || len(result.Dependencies) != 1 || len(result.Dependencies[0].Injections) != 1 || result.Dependencies[0].Injections[0].SymbolicSource != serviceconfigurationv1.SourcePostgresPDODSN {
		t.Fatalf("result=%+v", result)
	}
}

func TestAmbiguousConnectionEvidenceBlocksReview(t *testing.T) {
	result := analyze(t, memoryRepository{"Dockerfile": "FROM scratch\nEXPOSE 8080", "main.go": `package main; func main(){ _ = os.Getenv("DB_CONNECTION_STRING") }`})
	found := false
	for _, issue := range result.Issues {
		found = found || issue.Code == "CONNECTION_DIALECT_REQUIRED" && issue.Blocking
	}
	if !found {
		t.Fatalf("issues=%+v", result.Issues)
	}
}

func TestLegacyConnectionSourceReanalysisCanonicalizesAndWarns(t *testing.T) {
	result := analyze(t, memoryRepository{
		".opsi/opsi-cd.yaml": `version: 2
resources:
  - {logicalName: database, type: postgres, managed: true, required: true}
services:
  - key: api
    build: {context: ., dockerfile: Dockerfile, platform: linux/amd64}
    runtime:
      port: 8080
      dependencies:
        - target: database
          protocol: postgres
          required: true
          injections:
            - {environmentName: DATABASE_URL, symbolicSource: connection.url}
`,
		"Dockerfile": "FROM scratch\nEXPOSE 8080",
	})
	if got := result.Dependencies[0].Injections[0].SymbolicSource; got != serviceconfigurationv1.SourcePostgresURI {
		t.Fatalf("reanalysis source=%q", got)
	}
	found := false
	for _, issue := range result.Issues {
		found = found || issue.Code == "CONNECTION_SOURCE_DEPRECATED" && !issue.Blocking
	}
	if !found {
		t.Fatalf("issues=%+v", result.Issues)
	}
}

func TestUnsafeTemplateIsBlockedWithoutEnteringAnalysisPlan(t *testing.T) {
	result := analyze(t, memoryRepository{
		".opsi/opsi-cd.yaml": `version: 2
resources:
  - {logicalName: database, type: postgres, managed: true, required: true}
services:
  - key: api
    build: {context: ., dockerfile: Dockerfile, platform: linux/amd64}
    runtime:
      port: 8080
      dependencies:
        - target: database
          protocol: postgres
          required: true
          injections:
            - environmentName: DB_DSN
              symbolicSource: connection.template
              template: password=must-not-leak
`,
		"Dockerfile": "FROM scratch\nEXPOSE 8080",
	})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-leak") || result.Dependencies[0].Injections[0].Template != "" {
		t.Fatalf("unsafe template entered analysis plan: %s", data)
	}
	found := false
	for _, issue := range result.Issues {
		found = found || issue.Code == "CONNECTION_MAPPING_INVALID" && issue.Blocking
	}
	if !found {
		t.Fatalf("issues=%+v", result.Issues)
	}
}
