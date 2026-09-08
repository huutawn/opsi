package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	acceptanceID      = "p07b3b1"
	acceptanceInitial = "inserted"
	acceptanceUpdated = "updated"
)

type databaseConfig struct {
	host     string
	port     string
	database string
	user     string
	password string
	url      string
}

type acceptanceEvidence struct {
	SelectOne int    `json:"select_1"`
	Inserted  string `json:"inserted"`
	Updated   string `json:"updated"`
	Reconnect string `json:"reconnect"`
}

func main() {
	config, err := loadDatabaseConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	evidence, err := exerciseDatabase(context.Background(), config)
	if err != nil {
		log.Fatal("database acceptance failed")
	}
	encodedEvidence, _ := json.Marshal(evidence)
	log.Printf("acceptance=%s", encodedEvidence)
	handler := http.NewServeMux()
	handler.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if value, err := readAcceptance(ctx, config); err != nil || value != acceptanceUpdated {
			http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	handler.HandleFunc("/evidence", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(evidence)
	})
	log.Fatal(http.ListenAndServe(":8080", handler))
}

func loadDatabaseConfig(getenv func(string) string) (databaseConfig, error) {
	values := map[string]string{}
	for _, name := range []string{"DATABASE_HOST", "DATABASE_PORT", "DATABASE_NAME", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_URL"} {
		values[name] = getenv(name)
		if values[name] == "" {
			return databaseConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	if port, err := strconv.ParseUint(values["DATABASE_PORT"], 10, 16); err != nil || port == 0 {
		return databaseConfig{}, errors.New("DATABASE_PORT is invalid")
	}
	parsed, err := url.Parse(values["DATABASE_URL"])
	if err != nil || parsed.Scheme != "postgres" || parsed.User == nil || parsed.Path != "/"+values["DATABASE_NAME"] || parsed.Query().Get("sslmode") != "disable" {
		return databaseConfig{}, errors.New("DATABASE_URL is invalid")
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.Hostname() != values["DATABASE_HOST"] || parsed.Port() != values["DATABASE_PORT"] || parsed.User.Username() != values["DATABASE_USER"] || password != values["DATABASE_PASSWORD"] {
		return databaseConfig{}, errors.New("DATABASE_URL does not match canonical DATABASE values")
	}
	return databaseConfig{host: values["DATABASE_HOST"], port: values["DATABASE_PORT"], database: values["DATABASE_NAME"], user: values["DATABASE_USER"], password: values["DATABASE_PASSWORD"], url: values["DATABASE_URL"]}, nil
}

func exerciseDatabase(ctx context.Context, config databaseConfig) (acceptanceEvidence, error) {
	connection, err := pgx.Connect(ctx, config.url)
	if err != nil {
		return acceptanceEvidence{}, err
	}
	var one int
	if err := connection.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		connection.Close(ctx)
		return acceptanceEvidence{}, err
	}
	if _, err := connection.Exec(ctx, "CREATE TABLE IF NOT EXISTS opsi_p07b3b1_acceptance (id text PRIMARY KEY, value text NOT NULL)"); err != nil {
		connection.Close(ctx)
		return acceptanceEvidence{}, err
	}
	if _, err := connection.Exec(ctx, "INSERT INTO opsi_p07b3b1_acceptance(id,value) VALUES ($1,$2) ON CONFLICT (id) DO UPDATE SET value=EXCLUDED.value", acceptanceID, acceptanceInitial); err != nil {
		connection.Close(ctx)
		return acceptanceEvidence{}, err
	}
	inserted, err := queryValue(ctx, connection)
	if err == nil {
		_, err = connection.Exec(ctx, "UPDATE opsi_p07b3b1_acceptance SET value=$1 WHERE id=$2", acceptanceUpdated, acceptanceID)
	}
	updated := ""
	if err == nil {
		updated, err = queryValue(ctx, connection)
	}
	connection.Close(ctx)
	if err != nil {
		return acceptanceEvidence{}, err
	}
	reconnected, err := readAcceptance(ctx, config)
	if err != nil {
		return acceptanceEvidence{}, err
	}
	return acceptanceEvidence{SelectOne: one, Inserted: inserted, Updated: updated, Reconnect: reconnected}, nil
}

func readAcceptance(ctx context.Context, config databaseConfig) (string, error) {
	connection, err := pgx.Connect(ctx, config.url)
	if err != nil {
		return "", err
	}
	defer connection.Close(ctx)
	return queryValue(ctx, connection)
}

func queryValue(ctx context.Context, connection *pgx.Conn) (string, error) {
	var value string
	err := connection.QueryRow(ctx, "SELECT value FROM opsi_p07b3b1_acceptance WHERE id=$1", acceptanceID).Scan(&value)
	return value, err
}
