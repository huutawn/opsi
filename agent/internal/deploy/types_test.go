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
	cfg := config.DeploymentConfig{Branch: "main"}
	if !ShouldDeploy(WebhookEvent{Ref: "refs/heads/main"}, cfg) {
		t.Fatal("expected main branch to deploy")
	}
	if ShouldDeploy(WebhookEvent{Ref: "refs/heads/dev"}, cfg) {
		t.Fatal("expected dev branch to be ignored")
	}
}

func TestRequestFromWebhook(t *testing.T) {
	req, err := RequestFromWebhook(WebhookEvent{RepoURL: "https://example.test/repo.git", Ref: "refs/heads/main", After: "abcdef1234567890", TriggeredBy: "github"}, config.DeploymentConfig{
		Branch:       "main",
		ServiceName:  "api",
		Namespace:    "default",
		ManifestPath: "k8s/deploy.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Branch != "main" || req.ImageTag != "api:abcdef123456" || req.TriggeredBy != "github" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestRequestValidateRejectsBadInputs(t *testing.T) {
	base := testRequest()
	base.Service = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing service error")
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
	base.Service = "bad/name"
	if err := base.Validate(); err == nil {
		t.Fatal("expected bad service error")
	}
}

func TestRequestFromContractFillsConfigDefaults(t *testing.T) {
	req, err := RequestFromContract(&agentv1.DeployRequest{Service: "api", GitSHA: "abcdef1234567890"}, config.DeploymentConfig{
		RepoURL:      "https://example.test/repo.git",
		Branch:       "main",
		Namespace:    "prod",
		ManifestPath: "k8s/deploy.yaml",
		Registry:     "registry.local:5000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ImageTag != "registry.local:5000/api:abcdef123456" || req.Namespace != "prod" {
		t.Fatalf("unexpected request: %+v", req)
	}
}
