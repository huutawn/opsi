package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func TestEvidenceReaderProjectsFactualRolloutWithoutMutation(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/deploy.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := testRuntimeSnapshot(t, "job-evidence", "d")
	intent := testRolloutIntent(t, "rollout-evidence", snapshot, nil)
	before, err := store.BeginRollout(context.Background(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	failure := deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, "workload-controlled detail", false)
	after, err := store.TransitionRollout(context.Background(), intent.RolloutID, deploymentv1.RolloutStateFailed, failure, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.ReadIncidentEvidence(context.Background(), intent.Target.ProjectID, intent.Target.ServiceKey, intent.CreatedAt.Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if projection == nil || projection.RolloutID != intent.RolloutID || projection.State != deploymentv1.RolloutStateFailed || projection.FailureCode != deploymentv1.RolloutCodeReadinessFailed || projection.DesiredDigest != snapshot.Image.Digest || projection.TotalEvents != 2 || len(projection.Events) != 2 {
		t.Fatalf("unexpected projection: %+v", projection)
	}
	persisted, err := store.GetRollout(context.Background(), intent.RolloutID)
	if err != nil || persisted.StateHash != after.StateHash || persisted.Version != after.Version || before.Version != 1 || strings.Contains(projection.FailureCode, "workload-controlled") {
		t.Fatalf("evidence read mutated or leaked rollout detail: persisted=%+v projection=%+v err=%v", persisted, projection, err)
	}
}
