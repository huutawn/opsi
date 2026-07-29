package agentv1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIncidentServiceExposesEvidenceRead(t *testing.T) {
	var methods []string
	for _, method := range IncidentService_ServiceDesc.Methods {
		methods = append(methods, method.MethodName)
	}
	want := []string{"ListIncidents", "GetIncident", "GetIncidentEvidence", "ResolveIncident"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("incident methods=%v want=%v", methods, want)
	}
}

func TestIncidentEvidenceContractContainsOnlyFactualFields(t *testing.T) {
	body, err := json.Marshal(IncidentEvidence{SchemaVersion: "opsi.incident_evidence.v1", ContentSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"rca_result", "mitigation_actions_json", "recommended_action", "authorization", "endpoint", "sqlite"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("contract contains forbidden field %q: %s", forbidden, body)
		}
	}
}
