package deploy

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifyGitHubSignature("secret", body, header) {
		t.Fatal("expected valid signature")
	}
	if VerifyGitHubSignature("secret", body, header[:len(header)-2]+"ff") {
		t.Fatal("expected invalid signature")
	}
	legacy := hmac.New(sha1.New, []byte("secret"))
	_, _ = legacy.Write(body)
	if !VerifyGitHubSignature("secret", body, "sha1="+hex.EncodeToString(legacy.Sum(nil))) {
		t.Fatal("expected valid sha1 signature")
	}
	if VerifyGitHubSignature("secret", body, "bad") {
		t.Fatal("expected unsupported signature prefix to fail")
	}
}

func TestShouldDeployFiltersBranch(t *testing.T) {
	cfg := config.DeploymentConfig{Branch: "main", WatchPaths: []string{"apps/api/**", "packages/shared/**"}}
	if !ShouldDeploy(WebhookEvent{Ref: "refs/heads/main"}, cfg) {
		t.Fatal("expected main branch with no changed files to deploy")
	}
	if ShouldDeploy(WebhookEvent{Ref: "refs/heads/dev"}, cfg) {
		t.Fatal("expected dev branch to be ignored")
	}
	if !ShouldDeploy(WebhookEvent{Ref: "refs/heads/main", Modified: []string{"apps/api/main.go"}}, cfg) {
		t.Fatal("expected watched path to deploy")
	}
	if ShouldDeploy(WebhookEvent{Ref: "refs/heads/main", Modified: []string{"docs/readme.md"}}, cfg) {
		t.Fatal("expected unrelated path to skip deploy")
	}
}

func TestWebhookChangedFilesParsesGitHubPayload(t *testing.T) {
	event := WebhookEvent{Body: []byte(`{"commits":[{"modified":["apps/api/main.go"],"added":["packages/shared/a.go"],"removed":["old.go"]}]}`)}
	files := event.ChangedFiles()
	if len(files) != 3 || files[0] != "apps/api/main.go" || files[1] != "packages/shared/a.go" || files[2] != "old.go" {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestClassifyFailure(t *testing.T) {
	if decision := ClassifyFailure("rollout timeout", false, 0); !decision.RollbackSafe || decision.FailType != FailTypeDeployTimeFail {
		t.Fatalf("expected deploy-time rollback safe: %+v", decision)
	}
	if decision := ClassifyFailure("OOMKilled", false, 0); decision.RollbackSafe || decision.FailType != FailTypeResourceExhaustion {
		t.Fatalf("expected resource exhaustion no rollback: %+v", decision)
	}
	if decision := ClassifyFailure("database connection refused", false, 0); decision.RollbackSafe || decision.FailType != FailTypeExternalDependency {
		t.Fatalf("expected dependency no rollback: %+v", decision)
	}
	if decision := ClassifyFailure("crashloop", true, 6); decision.RollbackSafe || decision.FailType != FailTypeRuntimeCrash {
		t.Fatalf("expected runtime no rollback: %+v", decision)
	}
	if decision := ClassifyRolloutFailure(RolloutFailure{Reason: "rollout failed", ContainerReasons: []string{"OOMKilled"}}); decision.RollbackSafe || decision.FailType != FailTypeResourceExhaustion {
		t.Fatalf("expected typed resource signal no rollback: %+v", decision)
	}
}

func TestRequestFromWebhook(t *testing.T) {
	req, err := RequestFromWebhook(WebhookEvent{RepoURL: "https://example.test/repo.git", Ref: "refs/heads/main", After: "abcdef1234567890", TriggeredBy: "github"}, config.DeploymentConfig{
		ProjectID:    "proj-dev",
		ServiceID:    "svc-api",
		Branch:       "main",
		ServiceName:  "api",
		ServiceType:  "backend",
		Namespace:    "default",
		ManifestPath: "k8s/deploy.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Branch != "main" || req.ImageTag != "proj-dev/api:abcdef123456" || req.TriggeredBy != "github" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestRequestValidateRejectsBadInputs(t *testing.T) {
	base := testRequest()
	base.ProjectID = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing project error")
	}
	base = testRequest()
	base.ProjectID = "../bad"
	if err := base.Validate(); err == nil {
		t.Fatal("expected bad project error")
	}
	base = testRequest()
	base.ServiceID = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing service id error")
	}
	base = testRequest()
	base.ServiceName = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing service name error")
	}
	base = testRequest()
	base.RepoURL = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing repo error")
	}
	base = testRequest()
	base.GitSHA = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing git sha error")
	}
	base = testRequest()
	base.ManifestPath = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing manifest error")
	}
	base = testRequest()
	base.ServiceName = "bad/name"
	if err := base.Validate(); err == nil {
		t.Fatal("expected bad service error")
	}
	base = testRequest()
	base.DependsOn = []ServiceDependency{{Name: "Bad_Name"}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected bad dependency error")
	}
	base = testRequest()
	base.DependsOn = []ServiceDependency{{Name: "mydb"}, {Name: "mydb"}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected duplicate dependency error")
	}
}

func TestRequestValidateRejectsUnsafeDeployPaths(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
	}{
		{"build context traversal", func(req *Request) { req.BuildContext = "../../etc" }},
		{"dockerfile absolute", func(req *Request) { req.Dockerfile = "/etc/passwd" }},
		{"manifest backslash traversal", func(req *Request) { req.ManifestPath = `..\..\windows` }},
		{"watch path traversal", func(req *Request) { req.WatchPaths = []string{"apps/api/**", "../secrets/**"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest()
			tc.mutate(&req)
			if err := req.Validate(); err == nil {
				t.Fatal("expected unsafe path to be rejected")
			}
		})
	}
}

func TestRequestFromContractFillsConfigDefaults(t *testing.T) {
	req, err := RequestFromContract(&agentv1.DeployRequest{ServiceName: "api", GitSHA: "abcdef1234567890"}, config.DeploymentConfig{
		ProjectID:    "proj-dev",
		ServiceID:    "svc-api",
		RepoURL:      "https://example.test/repo.git",
		Branch:       "main",
		Namespace:    "prod",
		ManifestPath: "k8s/deploy.yaml",
		Registry:     "registry.local:5000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ImageTag != "registry.local:5000/proj-dev/api:abcdef123456" || req.Namespace != "prod" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.TerminationGracePeriodSeconds != DefaultTerminationGracePeriodSeconds || req.ResourceRequestsJSON != DefaultResourceRequestsJSON || req.ResourceLimitsJSON != DefaultResourceLimitsJSON {
		t.Fatalf("missing safe defaults: %+v", req)
	}
}
