package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestServiceConfigurationReviewedApplyReloadsWithoutDeployment(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-config", "Config", "config", "user-1", "project-config")
	if err != nil {
		t.Fatal(err)
	}
	source, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "web", ContainerPort: 3000}, "source")
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "api", ContainerPort: 8080}, "target")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := server.Registry.GetServiceConfiguration(project.ID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{Bindings: []registry.ServiceBinding{{Kind: registry.ServiceBindingInternalHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvPrefix: "API"}}}

	preview := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+source.ID+"/configuration/preview", draft, "")
	if preview.Code != http.StatusOK || !bytes.Contains(preview.Body.Bytes(), []byte(`"API_URL"`)) {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	diff := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+source.ID+"/configuration/diff", draft, "")
	if diff.Code != http.StatusOK || !bytes.Contains(diff.Body.Bytes(), []byte(`"connection"`)) || !bytes.Contains(diff.Body.Bytes(), []byte(`"generated_environment"`)) {
		t.Fatalf("diff status=%d body=%s", diff.Code, diff.Body.String())
	}
	apply := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+source.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: configuration.Revision, ExpectedStateHash: configuration.StateHash}, "apply-1")
	if apply.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	reload := configurationRequest(t, server, http.MethodGet, "/api/projects/"+project.ID+"/services/"+source.ID+"/configuration", nil, "")
	if reload.Code != http.StatusOK || !bytes.Contains(reload.Body.Bytes(), []byte(`"revision":1`)) {
		t.Fatalf("reload status=%d body=%s", reload.Code, reload.Body.String())
	}
	stale := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+source.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: configuration.Revision, ExpectedStateHash: configuration.StateHash}, "apply-2")
	if stale.Code != http.StatusConflict || !bytes.Contains(stale.Body.Bytes(), []byte(`SERVICE_CONFIGURATION_STALE`)) {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	deployments, err := server.Registry.ListDeployments(project.ID)
	if err != nil || len(deployments) != 0 {
		t.Fatalf("configuration mutated deployments: %+v err=%v", deployments, err)
	}
}

func configurationRequest(t *testing.T, server *Server, method, path string, body any, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
		request.Header.Set("X-Request-ID", "request-config")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
