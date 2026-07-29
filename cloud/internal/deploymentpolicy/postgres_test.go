package deploymentpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func TestPostgresPlacementDurabilityConcurrencyAndRouting(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPSI_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	ids := seedPlacementFixture(t, db, suffix)
	now := time.Now().UTC()
	registryStore := registry.PostgresService{DB: db, Now: func() time.Time { return now }}
	topologyService := topology.Service{Store: topology.PostgresStore{DB: db}, Facts: registryStore, Now: func() time.Time { return now }, ReservedCPU: 100, ReservedMemory: 64 << 20}
	topologyDraft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: ids.project, Assignments: []topologyv1.Assignment{{ServiceKey: "api", EnvironmentID: ids.environment, RuntimeID: ids.runtime, Replicas: 1, CPURequestMillicores: 200, MemoryRequestBytes: 128 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	initialRequest := topologyv1.ApplyRequest{Draft: topologyDraft}
	initial, err := topologyService.Apply(ctx, ids.project, ids.user, "topology-initial-"+suffix, initialRequest, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := topologyService.Apply(ctx, ids.project, ids.user, "topology-initial-"+suffix, initialRequest, false)
	if err != nil || !replay.Reused || replay.Plan.ID != initial.Plan.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflicting := initialRequest
	conflicting.Draft.Assignments[0].Replicas = 2
	if _, err = topologyService.Apply(ctx, ids.project, ids.user, "topology-initial-"+suffix, conflicting, false); topologyErrorCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("err=%v", err)
	}
	record := seedBuildRecord(t, db, ids, now)
	policyService := Service{Store: PostgresStore{DB: db}, BuildRecords: buildrecord.PostgresStore{DB: db}, Bindings: registryStore, Topology: topologyService, Now: func() time.Time { return now }}
	policyDraft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: ids.project, RepositoryID: uint64(ids.repository), ServiceKeys: []string{"api"}, WorkflowRefs: []string{record.Workload.WorkflowRef}, AllowedEvents: []string{record.Workload.EventName}, AllowedGitRefs: []string{record.Workload.Ref}, EnvironmentID: ids.environment, AllowedRuntimeIDs: []string{ids.runtime}, AllowedOCIRepositories: []string{record.Build.OCIRepository}, AllowedPlatforms: []string{record.Build.Platform}, AllowedConfigHashes: []string{record.Build.ConfigHash}, AllowedBuildPlanHashes: []string{record.Build.PlanHash}, Enabled: true}
	policy, err := policyService.Apply(ctx, ids.project, ids.user, "policy-initial-"+suffix, deploymentpolicyv1.ApplyRequest{Draft: policyDraft})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := policyService.Route(ctx, ids.project, deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: ids.environment})
	if err != nil || !decision.Eligible || decision.AgentID != ids.agent {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registryStore = registry.PostgresService{DB: db, Now: func() time.Time { return now }}
	topologyService = topology.Service{Store: topology.PostgresStore{DB: db}, Facts: registryStore, Now: func() time.Time { return now }, ReservedCPU: 100, ReservedMemory: 64 << 20}
	policyService = Service{Store: PostgresStore{DB: db}, BuildRecords: buildrecord.PostgresStore{DB: db}, Bindings: registryStore, Topology: topologyService, Now: func() time.Time { return now }}
	persisted, err := topologyService.Get(ctx, ids.project)
	if err != nil || persisted.ID != initial.Plan.ID {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	persistedPolicy, err := policyService.Get(ctx, ids.project, policy.Policy.ID)
	if err != nil || persistedPolicy.PolicyHash != policy.Policy.PolicyHash {
		t.Fatalf("policy=%+v err=%v", persistedPolicy, err)
	}
	request := topologyv1.ApplyRequest{Draft: topologyDraft, ExpectedRevision: persisted.Revision, ExpectedStateHash: persisted.StateHash}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"topology-concurrent-a-" + suffix, "topology-concurrent-b-" + suffix} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := topologyService.Apply(ctx, ids.project, ids.user, key, request, false)
			errs <- err
		}(key)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if topologyErrorCode(err) == "TOPOLOGY_STATE_CONFLICT" {
			conflict++
		} else {
			t.Fatalf("unexpected err=%v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	decision, err = policyService.Route(ctx, ids.project, deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: ids.environment})
	if err != nil || !decision.Eligible {
		t.Fatalf("restart decision=%+v err=%v", decision, err)
	}
	if _, err = db.Exec(`UPDATE topology_plan_revisions SET plan_hash=$1 WHERE id=$2`, strings.Repeat("f", 64), persisted.ID); err == nil {
		t.Fatal("topology revision update unexpectedly succeeded")
	}
	if _, err = db.Exec(`DELETE FROM deployment_policy_revisions WHERE id=$1`, policy.Policy.ID); err == nil {
		t.Fatal("policy revision delete unexpectedly succeeded")
	}
}

type placementIDs struct {
	org, user, project, environment, runtime, node, agent, service, binding string
	repository, installation, owner                                         int64
}

func seedPlacementFixture(t *testing.T, db *sql.DB, suffix string) placementIDs {
	t.Helper()
	numeric := time.Now().UnixNano()
	ids := placementIDs{org: "org-" + suffix, user: "user-" + suffix, project: "proj-" + suffix, environment: "env-" + suffix, runtime: "rt-" + suffix, node: "node-" + suffix, agent: "agent-" + suffix, service: "svc-" + suffix, binding: "bind-" + suffix, repository: numeric, installation: numeric - 1, owner: numeric - 2}
	now := time.Now().UTC()
	statements := []struct {
		q    string
		args []any
	}{{`INSERT INTO users(id,email,created_at) VALUES($1,$2,$3)`, []any{ids.user, ids.user + "@example.test", now}}, {`INSERT INTO organizations(id,name,slug,status,created_at,updated_at) VALUES($1,$2,$3,'active',$4,$4)`, []any{ids.org, "org", ids.org, now}}, {`INSERT INTO projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES($1,$2,'p',$1,'ready',$3,$4,$4)`, []any{ids.project, ids.org, ids.user, now}}, {`INSERT INTO project_memberships(project_id,user_id,role,created_at) VALUES($1,$2,'owner',$3)`, []any{ids.project, ids.user, now}}, {`INSERT INTO environments(id,org_id,project_id,name,type,status,created_at,updated_at) VALUES($1,$2,$3,'prod','prod','active',$4,$4)`, []any{ids.environment, ids.org, ids.project, now}}, {`INSERT INTO runtimes(id,org_id,project_id,environment_id,name,type,status,created_at,updated_at) VALUES($1,$2,$3,$4,'primary','k3s','ready',$5,$5)`, []any{ids.runtime, ids.org, ids.project, ids.environment, now}}, {`INSERT INTO nodes(id,org_id,project_id,environment_id,runtime_id,name,role,status,cpu_cores,memory_mb,k3s_status,agent_id,last_seen_at,last_inventory_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'node','server','healthy',2,2048,'ready',$6,$7,$7,$7,$7)`, []any{ids.node, ids.org, ids.project, ids.environment, ids.runtime, ids.agent, now}}, {`INSERT INTO agents(id,org_id,project_id,runtime_id,node_id,public_key_fingerprint,credential_hash,version,capabilities,status,last_seen_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'fp','hash','test','{"deploy":true}'::jsonb,'active',$6,$6,$6)`, []any{ids.agent, ids.org, ids.project, ids.runtime, ids.node, now}}, {`INSERT INTO control_services(id,org_id,project_id,environment_id,runtime_id,name,type,status,source_type,namespace,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'api','application','active','image','default',$6,$6)`, []any{ids.service, ids.org, ids.project, ids.environment, ids.runtime, now}}, {`INSERT INTO github_installations(installation_id,account_id,account_login,account_type,status,suspended,created_at,updated_at) VALUES($1,$2,'owner','User','active',false,$3,$3)`, []any{ids.installation, ids.owner, now}}, {`INSERT INTO github_repositories(repository_id,installation_id,owner_id,owner_login,name,full_name,private,archived,disabled,default_branch,status,created_at,updated_at) VALUES($1,$2,$3,'owner','repo','owner/repo',false,false,false,'main','active',$4,$4)`, []any{ids.repository, ids.installation, ids.owner, now}}, {`INSERT INTO github_repository_claims(repository_id,installation_id,project_id,claimed_by,status,claimed_at) VALUES($1,$2,$3,$4,'active',$5)`, []any{ids.repository, ids.installation, ids.project, ids.user, now}}, {`INSERT INTO github_service_bindings(id,project_id,service_id,repository_id,installation_id,service_key,config_path,status,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'api','.opsi/opsi-cd.yaml','active',$6,$7,$7)`, []any{ids.binding, ids.project, ids.service, ids.repository, ids.installation, ids.user, now}}}
	for _, statement := range statements {
		if _, err := db.Exec(statement.q, statement.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return ids
}

func seedBuildRecord(t *testing.T, db *sql.DB, ids placementIDs, now time.Time) buildrecordv1.Record {
	t.Helper()
	record := buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-" + ids.project, ProjectID: ids.project, RepositoryID: uint64(ids.repository), RepositoryOwnerID: uint64(ids.owner), ActiveBindingID: ids.binding, ServiceID: ids.service, ServiceKey: "api", CreatedAt: now, Workload: buildrecordv1.WorkloadIdentity{Issuer: "https://token.actions.githubusercontent.com", Subject: "repo:owner/repo", RepositoryID: uint64(ids.repository), RepositoryOwnerID: uint64(ids.owner), Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), EventName: "push", Workflow: "cd", WorkflowRef: "owner/repo/.github/workflows/cd.yml@refs/heads/main", RunID: uint64(now.UnixNano()), RunAttempt: 1}, Build: buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("b", 64), PlanHash: strings.Repeat("c", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/owner/repo/api", OCIDigest: "sha256:" + strings.Repeat("d", 64), Status: "succeeded"}}
	if _, _, err := (buildrecord.PostgresStore{DB: db}).Create(context.Background(), strings.Repeat("e", 64), record); err != nil {
		t.Fatal(err)
	}
	return record
}
func topologyErrorCode(err error) string {
	var value topology.Error
	if errors.As(err, &value) {
		return value.Code
	}
	return ""
}
