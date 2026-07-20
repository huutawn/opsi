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
	}
	for index, mutate := range cases {
		spec := validWorkload()
		mutate(&spec)
		if spec.Validate() == nil {
			t.Fatalf("unsafe workload case %d was accepted", index)
		}
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
