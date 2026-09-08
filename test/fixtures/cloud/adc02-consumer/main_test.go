package main

import (
	"os"
	"testing"
)

func TestADC02Consumer_ConfigValidation(t *testing.T) {
	// 1. Unset env vars -> error
	os.Unsetenv("APP_DATABASE_URL")
	os.Unsetenv("APP_REDIS_URL")
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("REDIS_URL")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error when no custom APP_ URLs are set")
	}

	// 2. Default standard DATABASE_URL should NOT be accepted if APP_DATABASE_URL is not set
	os.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/opsi")
	_, err = loadConfig()
	if err == nil {
		t.Fatal("expected error when only DATABASE_URL is set (custom APP_DATABASE_URL is required)")
	}
	os.Unsetenv("DATABASE_URL")

	// 3. Setting APP_DATABASE_URL succeeds
	os.Setenv("APP_DATABASE_URL", "postgres://user:pass@localhost:5432/opsi")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error with APP_DATABASE_URL: %v", err)
	}
	if cfg.databaseURL != "postgres://user:pass@localhost:5432/opsi" {
		t.Fatalf("unexpected database URL: %s", cfg.databaseURL)
	}
	os.Unsetenv("APP_DATABASE_URL")

	// 4. Setting APP_REDIS_URL succeeds
	os.Setenv("APP_REDIS_URL", "redis://:pass@localhost:6379")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("unexpected error with APP_REDIS_URL: %v", err)
	}
	if cfg.redisURL != "redis://:pass@localhost:6379" {
		t.Fatalf("unexpected redis URL: %s", cfg.redisURL)
	}
	os.Unsetenv("APP_REDIS_URL")
}
