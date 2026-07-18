package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type runnerResult struct {
	output []byte
	err    error
}
type scriptedRunner struct {
	results []runnerResult
	calls   [][]string
}

func (r *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(r.results) == 0 {
		return nil, errors.New("unexpected command")
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result.output, result.err
}

func TestChangedServicePlanDependencySharedRenameAndHash(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	api := testService("api", "Dockerfile")
	api.Build.Context = "api"
	api.Build.Dockerfile = "api/Dockerfile"
	api.SharedPaths = []string{"shared"}
	worker := testService("worker", "Dockerfile")
	worker.Build.Context = "worker"
	worker.Build.Dockerfile = "worker/Dockerfile"
	worker.SharedPaths = []string{"shared"}
	worker.Dependencies = []string{"api"}
	for _, dir := range []string{"api", "worker", "shared"} {
		if err := osMkdir(root, dir); err != nil {
			t.Fatal(err)
		}
	}
	writeDockerfile(t, root, "api/Dockerfile")
	writeDockerfile(t, root, "worker/Dockerfile")
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{worker, api}}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	runner := trustedDiffRunner([]byte("M\x00api/main.go\x00"))
	plan, err := (CDService{Runner: runner}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.AffectedServiceKeys, []string{"api", "worker"}) || plan.FullBuild {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Services[1].Reasons[0].Code != "dependency_changed" || len(plan.PlanHash) != 64 {
		t.Fatalf("reasons=%+v hash=%q", plan.Services, plan.PlanHash)
	}
	secondRunner := trustedDiffRunner([]byte("M\x00api/main.go\x00"))
	second, _ := (CDService{Runner: secondRunner}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if plan.PlanHash != second.PlanHash {
		t.Fatal("plan hash is not deterministic")
	}
	sharedPlan, _ := (CDService{Runner: trustedDiffRunner([]byte("M\x00shared/schema.json\x00"))}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !reflect.DeepEqual(sharedPlan.AffectedServiceKeys, []string{"api", "worker"}) {
		t.Fatalf("shared=%+v", sharedPlan)
	}
	renamePlan, _ := (CDService{Runner: trustedDiffRunner([]byte("R100\x00api/old.go\x00worker/new.go\x00"))}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !reflect.DeepEqual(renamePlan.AffectedServiceKeys, []string{"api", "worker"}) {
		t.Fatalf("rename=%+v", renamePlan)
	}
}

func TestPlanEmptyMissingShallowMalformedAndPrefixCollision(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	api := testService("api", "Dockerfile")
	api.Build.Context = "api"
	api.Build.Dockerfile = "Dockerfile"
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{api}}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	empty, err := (CDService{Runner: trustedDiffRunner(nil)}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if err != nil || empty.FullBuild || len(empty.AffectedServiceKeys) != 0 || empty.ReasonCodes[0] != "no_changes" {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	prefix, _ := (CDService{Runner: trustedDiffRunner([]byte("M\x00api-old/main.go\x00"))}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !prefix.FullBuild || !reflect.DeepEqual(prefix.AffectedServiceKeys, []string{"api"}) || prefix.ReasonCodes[0] != "unmatched_changes" {
		t.Fatalf("prefix collision was not handled fail-safe: %+v", prefix)
	}
	missing, _ := (CDService{}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: "", Head: head, Repository: root, Config: cfg})
	if !missing.FullBuild || missing.ReasonCodes[0] != "base_missing" {
		t.Fatalf("missing=%+v", missing)
	}
	shallowRunner := &scriptedRunner{results: []runnerResult{{output: []byte("true\n")}}}
	shallow, _ := (CDService{Runner: shallowRunner}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !shallow.FullBuild || shallow.ReasonCodes[0] != "shallow_repository" {
		t.Fatalf("shallow=%+v", shallow)
	}
	malformed, _ := (CDService{Runner: trustedDiffRunner([]byte("R100\x00only-old\x00"))}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !malformed.FullBuild || malformed.ReasonCodes[0] != "diff_invalid" {
		t.Fatalf("malformed=%+v", malformed)
	}
	overflow, _ := (CDService{Runner: trustedDiffRunner([]byte("M\x00api/file.go\x00")), Limits: PlannerLimits{MaxDiffBytes: 4, MaxChangedPaths: 10, MaxServices: 10, MaxDependencyDepth: 10}}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: cfg})
	if !overflow.FullBuild || overflow.ReasonCodes[0] != "diff_limit_exceeded" {
		t.Fatalf("overflow=%+v", overflow)
	}
	initial, _ := (CDService{}).Plan(context.Background(), PlanRequest{Event: EventInitial, Head: head, Repository: root, Config: cfg})
	if !initial.FullBuild || initial.ReasonCodes[0] != "initial_build" {
		t.Fatalf("initial=%+v", initial)
	}
}

func TestParseNameStatusHandlesAllSupportedRecords(t *testing.T) {
	paths, err := parseNameStatus([]byte("A\x00added\x00M\x00modified\x00D\x00deleted\x00T\x00typed\x00C075\x00copy-old\x00copy-new\x00R100\x00rename-old\x00rename-new\x00"), 20)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"added", "copy-new", "copy-old", "deleted", "modified", "rename-new", "rename-old", "typed"}
	if !reflect.DeepEqual(paths, expected) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestPlanSupportsPushPullRequestAndMergeEvents(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{testService("api", "Dockerfile")}}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, event := range []EventType{EventPush, EventPullRequest, EventMerge} {
		plan, err := (CDService{Runner: trustedDiffRunner([]byte("M\x00Dockerfile\x00"))}).Plan(context.Background(), PlanRequest{Event: event, Base: base, Head: head, Repository: root, Config: cfg})
		if err != nil || !reflect.DeepEqual(plan.AffectedServiceKeys, []string{"api"}) {
			t.Fatalf("event=%s plan=%+v err=%v", event, plan, err)
		}
	}
}

func TestPlanExpandsTransitiveDependentClosure(t *testing.T) {
	root := t.TempDir()
	services := []ServiceV2{}
	for _, key := range []string{"api", "worker", "web"} {
		if err := osMkdir(root, key); err != nil {
			t.Fatal(err)
		}
		writeDockerfile(t, root, key+"/Dockerfile")
		service := testService(key, key+"/Dockerfile")
		service.Build.Context = key
		services = append(services, service)
	}
	services[1].Dependencies = []string{"api"}
	services[2].Dependencies = []string{"worker"}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	plan, err := (CDService{Runner: trustedDiffRunner([]byte("D\x00api/Dockerfile\x00"))}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: ConfigV2{Version: 2, Services: services}})
	if err != nil || !reflect.DeepEqual(plan.AffectedServiceKeys, []string{"api", "web", "worker"}) {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	overflow, err := (CDService{Runner: trustedDiffRunner([]byte("D\x00api/Dockerfile\x00")), Limits: PlannerLimits{MaxDiffBytes: 1024, MaxChangedPaths: 10, MaxServices: 10, MaxDependencyDepth: 1}}).Plan(context.Background(), PlanRequest{Event: EventPush, Base: base, Head: head, Repository: root, Config: ConfigV2{Version: 2, Services: services}})
	if err != nil || !overflow.FullBuild || overflow.ReasonCodes[0] != "dependency_depth_exceeded" {
		t.Fatalf("overflow=%+v err=%v", overflow, err)
	}
}

func trustedDiffRunner(diff []byte) *scriptedRunner {
	return &scriptedRunner{results: []runnerResult{{output: []byte("false\n")}, {}, {}, {output: diff}}}
}
func osMkdir(root, name string) error { return os.MkdirAll(filepath.Join(root, name), 0o755) }
