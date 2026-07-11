package agentv1

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func TestProtoServiceRPCContractDrift(t *testing.T) {
	data, err := os.ReadFile("../../agent/v1/status.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := protoRPCs(string(data))
	got := map[string][]string{
		StatusServiceName:         descMethods(StatusService_ServiceDesc),
		DeploymentServiceName:     descMethods(DeploymentService_ServiceDesc),
		ServiceManagerServiceName: descMethods(ServiceManagerService_ServiceDesc),
		TelemetryServiceName:      descMethods(TelemetryService_ServiceDesc),
		SecretServiceName:         descMethods(SecretService_ServiceDesc),
		IncidentServiceName:       descMethods(IncidentService_ServiceDesc),
	}
	if len(want) != len(got) {
		t.Fatalf("service count drift: proto=%v binding=%v", keys(want), keys(got))
	}
	for service, wantMethods := range want {
		gotMethods, ok := got["opsi.agent.v1."+service]
		if !ok {
			t.Fatalf("missing binding service %s", service)
		}
		if strings.Join(wantMethods, ",") != strings.Join(gotMethods, ",") {
			t.Fatalf("%s rpc drift: proto=%v binding=%v", service, wantMethods, gotMethods)
		}
	}
}

func TestDeployRequestContractDrift(t *testing.T) {
	data, err := os.ReadFile("../../agent/v1/status.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := protoMessageFields(string(data), "DeployRequest")
	typ := reflect.TypeOf(DeployRequest{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Fatalf("DeployRequest field drift: proto=%v binding=%v", want, got)
	}
	proto := string(data)
	if !regexp.MustCompile(`reserved\s+19\s*;`).MatchString(proto) || !regexp.MustCompile(`reserved\s+"ingress_enabled"\s*;`).MatchString(proto) {
		t.Fatal("DeployRequest must reserve removed field number 19 and name ingress_enabled")
	}
}

func protoRPCs(src string) map[string][]string {
	out := map[string][]string{}
	serviceRE := regexp.MustCompile(`(?s)service\s+(\w+)\s*\{(.*?)\}`)
	rpcRE := regexp.MustCompile(`rpc\s+(\w+)\s*\(`)
	for _, service := range serviceRE.FindAllStringSubmatch(src, -1) {
		for _, rpc := range rpcRE.FindAllStringSubmatch(service[2], -1) {
			out[service[1]] = append(out[service[1]], rpc[1])
		}
		sort.Strings(out[service[1]])
	}
	return out
}

func protoMessageFields(src, messageName string) []string {
	messageRE := regexp.MustCompile(`(?s)message\s+` + regexp.QuoteMeta(messageName) + `\s*\{(.*?)\}`)
	match := messageRE.FindStringSubmatch(src)
	if len(match) != 2 {
		return nil
	}
	fieldRE := regexp.MustCompile(`(?m)^\s*(?:repeated\s+)?[\w.]+\s+(\w+)\s*=\s*\d+\s*;`)
	fields := make([]string, 0)
	for _, field := range fieldRE.FindAllStringSubmatch(match[1], -1) {
		fields = append(fields, field[1])
	}
	sort.Strings(fields)
	return fields
}

func descMethods(desc grpc.ServiceDesc) []string {
	var out []string
	for _, method := range desc.Methods {
		out = append(out, method.MethodName)
	}
	for _, stream := range desc.Streams {
		out = append(out, stream.StreamName)
	}
	sort.Strings(out)
	return out
}

func keys(values map[string][]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
