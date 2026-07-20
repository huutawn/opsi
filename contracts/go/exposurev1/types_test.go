package exposurev1

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validSpec(t *testing.T) ExposureSpec {
	t.Helper()
	spec, err := (ExposureSpec{
		SchemaVersion:   SchemaVersion,
		ProjectID:       "proj-1",
		EnvironmentID:   "env-prod",
		RuntimeID:       "runtime-1",
		ServiceKey:      "api",
		DeploymentJobID: "dep-1",
		Hostname:        "api.example.com",
		Path:            "/api",
		ServicePort:     8080,
		TLS:             TLSConfig{Mode: TLSDisabled},
		Metadata:        &Metadata{DisplayName: "Public API", Rationale: "customer traffic"},
	}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestExposureSpecStrictJSONAndDeterministicHash(t *testing.T) {
	spec := validSpec(t)
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStrictJSON(data)
	if err != nil || !reflect.DeepEqual(decoded, spec) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	reordered := `{"tls":{"mode":"disabled"},"service_port":8080,"path":"/api/","hostname":"API.EXAMPLE.COM","deployment_job_id":"dep-1","service_key":"api","runtime_id":"runtime-1","environment_id":"env-prod","project_id":"proj-1","schema_version":"opsi.exposure_spec/v1","metadata":{"rationale":"customer traffic","display_name":"Public API"},"spec_hash":"` + spec.SpecHash + `"}`
	canonical, err := DecodeStrictJSON([]byte(reordered))
	if err != nil || canonical.SpecHash != spec.SpecHash || canonical.Hostname != "api.example.com" || canonical.Path != "/api" {
		t.Fatalf("canonical=%+v err=%v", canonical, err)
	}
}

func TestExposureSpecRejectsUnknownOversizedAndUnsafeShapes(t *testing.T) {
	spec := validSpec(t)
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"annotations":{}`, `"middleware":"raw"`, `"manifest":"apiVersion: v1"`} {
		unknown := strings.Replace(string(data), `"spec_hash"`, field+`,"spec_hash"`, 1)
		if _, err := DecodeStrictJSON([]byte(unknown)); !hasCode(err, CodeInvalidJSON) {
			t.Fatalf("unknown field %s err=%v", field, err)
		}
	}
	if _, err := DecodeStrictJSON([]byte("apiVersion: networking.k8s.io/v1")); !hasCode(err, CodeInvalidJSON) {
		t.Fatalf("raw YAML err=%v", err)
	}
	if _, err := DecodeStrictJSON([]byte(strings.Repeat(" ", MaxJSONBytes+1))); !hasCode(err, CodeInvalidJSON) {
		t.Fatalf("oversized document err=%v", err)
	}
	for _, mutate := range []func(*ExposureSpec){
		func(value *ExposureSpec) { value.SchemaVersion = "opsi.exposure_spec/v2" },
		func(value *ExposureSpec) { value.ServicePort = 0 },
		func(value *ExposureSpec) { value.TLS = TLSConfig{Mode: TLSDisabled, SecretReference: "tls-1"} },
		func(value *ExposureSpec) { value.TLS = TLSConfig{Mode: TLSSecretRef, SecretReference: "raw/name"} },
		func(value *ExposureSpec) { value.Metadata.DisplayName = strings.Repeat("x", 129) },
		func(value *ExposureSpec) { value.SpecHash = strings.Repeat("0", 64) },
	} {
		candidate := spec
		metadata := *spec.Metadata
		candidate.Metadata = &metadata
		mutate(&candidate)
		if _, err := candidate.Canonicalize(); err == nil {
			t.Fatalf("accepted unsafe ExposureSpec: %+v", candidate)
		}
	}
}

func TestNormalizeHostnameMatrix(t *testing.T) {
	for input, want := range map[string]string{
		"api.example.com": "api.example.com",
		"API.Example.COM": "api.example.com",
	} {
		got, err := NormalizeHostname(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeHostname(%q)=%q err=%v", input, got, err)
		}
	}
	invalid := []string{
		"", "https://api.example.com", "api.example.com:443", "api.example.com/path",
		"api.example.com?q=1", "api.example.com#fragment", "127.0.0.1", "localhost",
		"svc.localhost", "*.example.com", ".example.com", "example.com.", "a..example.com",
		"-api.example.com", "api_.example.com", "tést.example.com",
		strings.Repeat("a", 64) + ".example.com", strings.Repeat("a", 250) + ".com",
	}
	for _, input := range invalid {
		if _, err := NormalizeHostname(input); !hasCode(err, CodeInvalidHostname) {
			t.Fatalf("NormalizeHostname(%q) err=%v", input, err)
		}
	}
}

func TestNormalizePathAndConflictMatrix(t *testing.T) {
	for input, want := range map[string]string{"/": "/", "/api": "/api", "/api/": "/api", "/api/v1": "/api/v1", "/café": "/café"} {
		got, err := NormalizePath(input)
		if err != nil || got != want {
			t.Fatalf("NormalizePath(%q)=%q err=%v", input, got, err)
		}
	}
	for _, input := range []string{"", "api", "/api//v1", "/api/./v1", "/api/../v1", `/api\\v1`, "/api?q=1", "/api#x", "/api/%2f/v1", "/api/%5C/v1", "/api/%2e%2e/v1", "/api\x00"} {
		if _, err := NormalizePath(input); !hasCode(err, CodeInvalidPath) {
			t.Fatalf("NormalizePath(%q) err=%v", input, err)
		}
	}
	for _, tc := range []struct {
		first, second string
		conflict      bool
	}{
		{"/api", "/api", true}, {"/api", "/api/v1", true}, {"/api", "/api2", false}, {"/", "/anything", true},
	} {
		if got := PathsConflict(tc.first, tc.second); got != tc.conflict {
			t.Fatalf("PathsConflict(%q,%q)=%v", tc.first, tc.second, got)
		}
	}
}

func hasCode(err error, code string) bool {
	var validation *ValidationError
	return errors.As(err, &validation) && validation.Code == code
}
