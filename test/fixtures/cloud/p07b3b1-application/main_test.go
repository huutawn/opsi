package main

import (
	"net/url"
	"testing"
)

func TestFixtureRequiresCanonicalDatabaseBinding(t *testing.T) {
	password := "binding-password"
	values := map[string]string{
		"DATABASE_HOST": "postgres.internal", "DATABASE_PORT": "5432", "DATABASE_NAME": "opsi",
		"DATABASE_USER": "opsi_b_role", "DATABASE_PASSWORD": password,
		"DATABASE_URL": "postgres://opsi_b_role:" + url.QueryEscape(password) + "@postgres.internal:5432/opsi?sslmode=disable",
	}
	config, err := loadDatabaseConfig(func(name string) string { return values[name] })
	if err != nil || config.database != "opsi" || config.user != "opsi_b_role" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	delete(values, "DATABASE_PASSWORD")
	if _, err := loadDatabaseConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("missing canonical binding value was accepted")
	}
}
