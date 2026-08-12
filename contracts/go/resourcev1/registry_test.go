package resourcev1

import "testing"

func TestInitialDefinitionsAreExplicitlyExperimental(t *testing.T) {
	want := map[Type]struct {
		port     int
		protocol Protocol
	}{
		TypePostgres: {5432, ProtocolPostgres},
		TypeRedis:    {6379, ProtocolRedis},
		TypeNATS:     {4222, ProtocolNATS},
		TypeRabbitMQ: {5672, ProtocolAMQP},
	}
	definitions := Definitions()
	if len(definitions) != len(want) {
		t.Fatalf("definitions=%d", len(definitions))
	}
	for resourceType, expected := range want {
		definition, ok := Definition(resourceType)
		if !ok || definition.SupportTier != SupportExperimental || definition.DefaultPort != expected.port || !Supports(resourceType, expected.protocol) {
			t.Fatalf("definition %q=%+v ok=%t", resourceType, definition, ok)
		}
	}
	unknown, ok := Definition("kafka")
	if ok || unknown.SupportTier != SupportUnsupported {
		t.Fatalf("unknown=%+v ok=%t", unknown, ok)
	}
}

func TestGeneratedValuesClassifySecrets(t *testing.T) {
	definitions := Definitions()
	definitions[0].Protocols[0] = ProtocolHTTP
	definition, _ := Definition(TypePostgres)
	values := map[string]ValueSensitivity{}
	for _, value := range definition.GeneratedValues {
		values[value.Name] = value.Sensitivity
	}
	if definition.Protocols[0] != ProtocolPostgres || values["HOST"] != ValueNonSecret || values["PORT"] != ValueNonSecret || values["PASSWORD"] != ValueSecret || values["URL"] != ValueSecret {
		t.Fatalf("values=%v", values)
	}
}
