package webhookrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

func TestRestoreReviewAPIAcceptsFactualPostgresVersion(t *testing.T) {
	server := NewServer(Config{})
	target := webhookReadyPostgres("project-1", "env-1", "runtime-1", "node-1", "agent-1")
	if _, _, err := server.Resources.Store.Create(context.Background(), target, "target", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	backup := backupv1.Backup{
		SchemaVersion: backupv1.SchemaVersion, ID: "bkp-1", ProjectID: target.ProjectID, EnvironmentID: target.EnvironmentID, SourceResourceID: "source", SourceNodeID: "node-1",
		ResourceType: target.Type, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: "18.6 (Debian 18.6-1.pgdg12+2)",
		SourceProfile: target.Runtime.Spec.Profile, SourceImage: target.Runtime.Spec.Image, SourceSpecRevision: 1, SourceSpecHash: strings.Repeat("b", 64), SourcePVCName: "source-pvc", SourcePVCUID: "source-pvc-uid", SourceStorageHash: strings.Repeat("c", 64),
		Format: backupv1.FormatCustom, Lifecycle: backupv1.LifecycleSucceeded, ArtifactSize: 64, SHA256: strings.Repeat("d", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArchiveVerified: true, CreatedAt: now, CompletedAt: &now,
	}
	if _, _, err := server.Backups.Store.Create(context.Background(), backup, "seed", "seed"); err != nil {
		t.Fatal(err)
	}

	response := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+target.ProjectID+"/backups/"+backup.ID+"/restore-review", `{"target_resource_id":"`+target.ID+`"}`, "review")
	if response.Code != http.StatusAccepted || strings.Contains(response.Body.String(), restorev1.FailureVersionUnsupported) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Review restorev1.Review `json:"review"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	created, err := server.Restores.GetReview(context.Background(), target.ProjectID, result.Review.ID)
	if err != nil || created.ID == "" || created.Lifecycle != restorev1.ReviewQueued || created.SourcePostgresVersion != backup.SourcePostgresVersion {
		t.Fatalf("review=%+v created=%+v err=%v", result.Review, created, err)
	}
}
