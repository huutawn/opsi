package resource

import (
	"context"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestWorkloadSecretIsStableScopedAndTransient(t *testing.T) {
	authority := NewMemoryCredentialAuthority()
	spec := resourcev1.WorkloadSecretSpec{CredentialID: "wsecret-stable", ProjectID: "project-1", ServiceID: "service-1"}
	first, err := authority.EnsureWorkloadSecret(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.EnsureWorkloadSecret(context.Background(), spec)
	if err != nil || first.Password != second.Password || first.Password == "" {
		t.Fatalf("credential was not generated idempotently: err=%v", err)
	}
	service := Service{Store: NewMemoryStore(), Scopes: testScopes{}, Credentials: authority}
	materials, err := service.ResolveSecretMaterials(context.Background(), "project-1", "service-1", []deploymentv1.SecretReference{{EnvName: "JWT_KEY", SecretID: spec.CredentialID}})
	if err != nil || len(materials) != 1 || materials[0].Values["JWT_KEY"] != first.Password {
		t.Fatalf("materials=%+v err=%v", materials, err)
	}
	if _, err := service.ResolveSecretMaterials(context.Background(), "project-2", "service-1", []deploymentv1.SecretReference{{EnvName: "JWT_KEY", SecretID: spec.CredentialID}}); err == nil {
		t.Fatal("cross-project workload secret resolution succeeded")
	}
	if _, err := service.ResolveSecretMaterials(context.Background(), "project-1", "service-2", []deploymentv1.SecretReference{{EnvName: "JWT_KEY", SecretID: spec.CredentialID}}); err == nil {
		t.Fatal("cross-service workload secret resolution succeeded")
	}
}

func TestPlannedWorkloadSecretBindsWithoutChangingOpaqueReferenceOrRevision(t *testing.T) {
	authority := NewMemoryCredentialAuthority()
	spec := resourcev1.WorkloadSecretUpsert{CredentialID: "wsecret-planned", ProjectID: "project-1", ServiceID: "planned:api", LogicalName: "oauth", Value: "one-way-value", IdempotencyKey: "upsert-planned"}
	metadata, _, err := authority.UpsertWorkloadSecret(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := authority.BindWorkloadSecret(context.Background(), "project-1", "planned:api", "service-1", "oauth")
	if err != nil || bound.ID != metadata.ID || bound.Reference != metadata.Reference || bound.Revision != metadata.Revision || bound.ServiceID != "service-1" {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	replayed, reused, err := authority.UpsertWorkloadSecret(context.Background(), spec)
	if err != nil || !reused || replayed.ID != bound.ID || replayed.ServiceID != bound.ServiceID || replayed.Revision != bound.Revision {
		t.Fatalf("replay after bind=%+v reused=%v err=%v", replayed, reused, err)
	}
	service := Service{Store: NewMemoryStore(), Scopes: testScopes{}, Credentials: authority}
	materials, err := service.ResolveSecretMaterials(context.Background(), "project-1", "service-1", []deploymentv1.SecretReference{{EnvName: "OAUTH_SECRET", SecretID: bound.ID}})
	if err != nil || len(materials) != 1 || materials[0].Values["OAUTH_SECRET"] != "one-way-value" {
		t.Fatalf("materials=%+v err=%v", materials, err)
	}
	if _, err := authority.GetWorkloadSecret(context.Background(), "project-1", "planned:api", "oauth"); err == nil {
		t.Fatal("planned scope remained after binding")
	}
}

func TestWorkloadSecretLogicalNameIsBounded(t *testing.T) {
	authority := NewMemoryCredentialAuthority()
	for _, name := range []string{"", " leading", "trailing ", "line\nbreak", string(make([]byte, 129))} {
		spec := resourcev1.WorkloadSecretUpsert{CredentialID: "wsecret-invalid", ProjectID: "project-1", ServiceID: "service-1", LogicalName: name, Value: "value", IdempotencyKey: "invalid-name"}
		if _, _, err := authority.UpsertWorkloadSecret(context.Background(), spec); err == nil {
			t.Fatalf("logical name %q should be rejected", name)
		}
	}
}
