package actiondevice

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresDeviceAndRevokeSurviveRestart(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("OPSI_TEST_DATABASE_URL is required")
		}
		t.Skip("OPSI_TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE IF EXISTS action_device_audit, action_devices`); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: NewPostgresStore(db)}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	principal := Principal{ProjectID: "p1", UserID: "u1", Role: "owner"}
	device, _, err := service.Register(context.Background(), principal, RegisterRequest{DisplayName: "laptop", PublicKey: publicKey, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Revoke(context.Background(), principal, device.ID); err != nil {
		t.Fatal(err)
	}
	service = Service{Store: NewPostgresStore(db)}
	reloaded, err := service.Get(context.Background(), principal, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != DeviceRevoked || string(reloaded.PublicKey) != string(publicKey) {
		t.Fatalf("reloaded device = %#v", reloaded)
	}
}
