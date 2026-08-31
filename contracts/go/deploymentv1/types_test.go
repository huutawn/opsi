package deploymentv1

import (
	"strings"
	"testing"
)

func validWorkload() WorkloadSpec {
	return WorkloadSpec{
		SchemaVersion:            WorkloadSchemaVersion,
		ServiceKey:               "api",
		Replicas:                 2,
		ApplicationContainerName: ApplicationContainer,
		ContainerPort:            8080,
		Resources: Resources{
			Requests: ResourceValues{CPU: "100m", Memory: "128Mi"},
			Limits:   ResourceValues{CPU: "500m", Memory: "512Mi"},
		},
		TerminationGracePeriodSecond: 30,
		Exposure:                     ExposureIntent{Mode: "internal"},
	}
}

func TestImmutableImageRejectsTagsAndPrefixConfusion(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	image, err := NewImmutableImage("ghcr.io/owner/app", digest)
	if err != nil {
		t.Fatal(err)
	}
	if !image.WithinPrefix("ghcr.io/owner/app") || image.WithinPrefix("ghcr.io/owner/ap") || image.WithinPrefix("ghcr.io/owner/app-evil") {
		t.Fatal("OCI prefix matching is not path-component aware")
	}
	for _, reference := range []ImmutableImage{
		{Repository: "ghcr.io/owner/app:latest", Digest: digest, Reference: "ghcr.io/owner/app:latest@" + digest},
		{Repository: "ghcr.io/owner/app", Digest: digest, Reference: "ghcr.io/owner/app:latest"},
		{Repository: "https://github.com/owner/app", Digest: digest, Reference: "https://github.com/owner/app@" + digest},
	} {
		if reference.Validate() == nil {
			t.Fatalf("accepted invalid immutable image: %+v", reference)
		}
	}
}

func TestWorkloadSpecRejectsUnsafeAndInlineSecretShapes(t *testing.T) {
	cases := []func(*WorkloadSpec){
		func(spec *WorkloadSpec) { spec.ApplicationContainerName = "sidecar" },
		func(spec *WorkloadSpec) { spec.Replicas = 0 },
		func(spec *WorkloadSpec) { spec.ContainerPort = 0 },
		func(spec *WorkloadSpec) {
			spec.Environment = []EnvironmentVariable{{Name: "TOKEN", Value: string([]byte{'x', 0, 'y'})}}
		},
		func(spec *WorkloadSpec) {
			spec.SecretReferences = []SecretReference{{EnvName: "TOKEN", SecretID: "inline/value"}}
		},
		func(spec *WorkloadSpec) { spec.Exposure.Mode = "internet" },
		func(spec *WorkloadSpec) {
			spec.Environment = []EnvironmentVariable{{Name: "API_TOKEN", Value: "inline-secret"}}
		},
		func(spec *WorkloadSpec) { spec.Resources.Limits.CPU = "50m" },
		func(spec *WorkloadSpec) { spec.Resources.Limits.Memory = "64Mi" },
		func(spec *WorkloadSpec) {
			spec.RegistryPullCredential = &RegistryPullCredentialReference{Provider: "ghcr", CredentialID: "hosted-opsi", Registry: "https://ghcr.io"}
		},
	}
	for index, mutate := range cases {
		spec := validWorkload()
		mutate(&spec)
		if spec.Validate() == nil {
			t.Fatalf("unsafe workload case %d was accepted", index)
		}
	}
}

func TestWorkloadSpecAcceptsBoundedStartupProbeAndHashesItsAuthority(t *testing.T) {
	first := validWorkload()
	first.StartupProbe = &Probe{Path: "/health", Port: 8080, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 60}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	firstHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	second := first
	probe := *first.StartupProbe
	probe.FailureThreshold = 61
	second.StartupProbe = &probe
	secondHash, err := second.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("startup probe edit did not invalidate the workload hash")
	}
	second.StartupProbe.FailureThreshold = 121
	if second.Validate() == nil {
		t.Fatal("unbounded startup probe was accepted")
	}
	second.StartupProbe = nil
	second.LivenessProbe = &Probe{Path: "/health", Port: 8080, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 11}
	if second.Validate() == nil {
		t.Fatal("runtime probe threshold was widened with the startup boundary")
	}
}

func TestValidateEnvironmentPreservesCaseSensitiveFrameworkMappings(t *testing.T) {
	if err := ValidateEnvironment(
		[]EnvironmentVariable{
			{Name: "Jwt__Issuer", Value: "identity-service"},
			{Name: "Jwt__Audience", Value: "identity-api"},
			{Name: "Jwt__AccessTokenMinutes", Value: "15"},
			{Name: "Jwt__RefreshTokenDays", Value: "30"},
		},
		[]SecretReference{{EnvName: "Jwt__SigningKey", SecretID: "wsecret-123"}},
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Database_Password", "Jwt__Key", "Jwt__SigningKey", "ConnectionStrings__Database"} {
		if err := ValidateEnvironment([]EnvironmentVariable{{Name: name, Value: "inline-secret"}}, nil); err == nil {
			t.Fatalf("secret-like environment name %q accepted an inline value", name)
		}
	}
}

func TestRegistryPullCredentialRequiresTypedNonSecretReference(t *testing.T) {
	ref := RegistryPullCredentialReference{Provider: "ghcr", CredentialID: "hosted-opsi", Registry: "ghcr.io"}
	spec := validWorkload()
	spec.RegistryPullCredential = &ref
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (RegistryPullCredential{Reference: ref, Username: "opsi-pull", Password: ""}).Validate(); err == nil {
		t.Fatal("empty registry token was accepted")
	}
}

func TestWorkloadHashNormalizesEnvironmentOrder(t *testing.T) {
	first := validWorkload()
	first.Environment = []EnvironmentVariable{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}
	second := validWorkload()
	second.Environment = []EnvironmentVariable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	firstHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("normalized hashes differ: %s != %s", firstHash, secondHash)
	}
}
