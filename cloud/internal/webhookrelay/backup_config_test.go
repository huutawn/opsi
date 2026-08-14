package webhookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupStoreConfigLoadsCredentialsFromFiles(t *testing.T) {
	dir := t.TempDir()
	accessFile, secretFile := filepath.Join(dir, "access"), filepath.Join(dir, "secret")
	if err := os.WriteFile(accessFile, []byte("access-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "cloud.json")
	config := `{"backup_store":{"endpoint":"https://s3.example.test","bucket":"backups","region":"test-1","access_key_file":"` + accessFile + `","secret_key_file":"` + secretFile + `"}}`
	if err := os.WriteFile(configFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BackupStore.ID != "default" || loaded.BackupStore.AccessKey != "access-key" || loaded.BackupStore.SecretKey != "secret-key" {
		t.Fatalf("store=%+v", loaded.BackupStore)
	}
}

func TestBackupStoreConfigRejectsImplicitInsecureTransport(t *testing.T) {
	cfg := Config{BackupStore: BackupStoreConfig{Endpoint: "http://127.0.0.1:9000", Bucket: "backups", Region: "test-1", AccessKeyFile: "access", SecretKeyFile: "secret", AccessKey: "access", SecretKey: "secret"}}
	if err := validateConfig(&cfg); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("err=%v", err)
	}
}

func TestBackupStoreConfigRejectsPartialConfiguration(t *testing.T) {
	for _, store := range []BackupStoreConfig{{ID: "only-id"}, {CAFile: "/tmp/only-ca"}, {SessionTokenFile: "/tmp/only-token"}, {AllowInsecure: true}} {
		cfg := Config{BackupStore: store}
		if err := validateBackupStoreConfig(&cfg); err == nil {
			t.Fatalf("partial backup store was ignored: %+v", store)
		}
	}
}
