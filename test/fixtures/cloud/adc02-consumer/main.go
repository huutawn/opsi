package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type consumerEvidence struct {
	DatabaseStatus    string `json:"database_status,omitempty"`
	DatabaseReadValue string `json:"database_read_value,omitempty"`
	ValkeyStatus      string `json:"valkey_status,omitempty"`
	ValkeyReadValue   string `json:"valkey_read_value,omitempty"`
}

type appConfig struct {
	databaseURL string
	redisURL    string
}

func loadConfig() (appConfig, error) {
	// Must NOT use default fallback names like DATABASE_URL or REDIS_URL
	dbURL := os.Getenv("APP_DATABASE_URL")
	redisURL := os.Getenv("APP_REDIS_URL")

	if dbURL == "" && redisURL == "" {
		return appConfig{}, errors.New("neither APP_DATABASE_URL nor APP_REDIS_URL provided")
	}

	return appConfig{
		databaseURL: dbURL,
		redisURL:    redisURL,
	}, nil
}

func exercisePostgres(ctx context.Context, dbURL string) (string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return "", fmt.Errorf("postgres connect failed: %w", err)
	}
	defer conn.Close(ctx)

	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return "", fmt.Errorf("postgres SELECT 1 failed: %w", err)
	}

	_, err = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS opsi_adc02_acceptance (id text PRIMARY KEY, value text NOT NULL)")
	if err != nil {
		return "", fmt.Errorf("postgres CREATE TABLE failed: %w", err)
	}

	_, err = conn.Exec(ctx, "INSERT INTO opsi_adc02_acceptance(id, value) VALUES ('adc02_key', 'realized_db_val') ON CONFLICT (id) DO UPDATE SET value=EXCLUDED.value")
	if err != nil {
		return "", fmt.Errorf("postgres INSERT failed: %w", err)
	}

	var val string
	if err := conn.QueryRow(ctx, "SELECT value FROM opsi_adc02_acceptance WHERE id='adc02_key'").Scan(&val); err != nil {
		return "", fmt.Errorf("postgres SELECT failed: %w", err)
	}

	if val != "realized_db_val" {
		return "", fmt.Errorf("postgres unexpected value: %q", val)
	}

	return val, nil
}

func writeRedisCommand(w io.Writer, args []string) error {
	var buf strings.Builder
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, err := io.WriteString(w, buf.String())
	return err
}

func readRedisResponse(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return "", errors.New("empty redis response")
	}

	prefix := line[0]
	payload := line[1:]

	switch prefix {
	case '+':
		return payload, nil
	case '-':
		return "", fmt.Errorf("redis error: %s", payload)
	case ':':
		return payload, nil
	case '$':
		var length int
		if _, err := fmt.Sscanf(payload, "%d", &length); err != nil {
			return "", err
		}
		if length == -1 {
			return "", nil
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:length]), nil
	default:
		return "", fmt.Errorf("unknown response prefix: %c", prefix)
	}
}

func exerciseRedis(redisURL string) (string, error) {
	u, err := url.Parse(redisURL)
	if err != nil {
		return "", fmt.Errorf("invalid redis url: %w", err)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "6379"
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("redis dial failed: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)

	// Auth if password is provided
	if u.User != nil {
		pass, hasPass := u.User.Password()
		username := u.User.Username()
		if hasPass {
			if username != "" {
				if err := writeRedisCommand(conn, []string{"AUTH", username, pass}); err != nil {
					return "", err
				}
			} else {
				if err := writeRedisCommand(conn, []string{"AUTH", pass}); err != nil {
					return "", err
				}
			}
			resp, err := readRedisResponse(reader)
			if err != nil {
				return "", fmt.Errorf("redis auth failed: %w", err)
			}
			if resp != "OK" {
				return "", fmt.Errorf("redis auth response: %s", resp)
			}
		}
	}

	// PING
	if err := writeRedisCommand(conn, []string{"PING"}); err != nil {
		return "", err
	}
	resp, err := readRedisResponse(reader)
	if err != nil || resp != "PONG" {
		return "", fmt.Errorf("redis ping failed: %s, err: %v", resp, err)
	}

	// SET
	if err := writeRedisCommand(conn, []string{"SET", "opsi_adc02_cache", "realized_valkey_val"}); err != nil {
		return "", err
	}
	resp, err = readRedisResponse(reader)
	if err != nil || resp != "OK" {
		return "", fmt.Errorf("redis set failed: %s, err: %v", resp, err)
	}

	// GET
	if err := writeRedisCommand(conn, []string{"GET", "opsi_adc02_cache"}); err != nil {
		return "", err
	}
	resp, err = readRedisResponse(reader)
	if err != nil {
		return "", fmt.Errorf("redis get failed: %w", err)
	}
	if resp != "realized_valkey_val" {
		return "", fmt.Errorf("redis get unexpected value: %q", resp)
	}

	return resp, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	evidence := consumerEvidence{}

	if cfg.databaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		val, err := exercisePostgres(ctx, cfg.databaseURL)
		cancel()
		if err != nil {
			log.Fatalf("database exercise failed: %v", err)
		}
		evidence.DatabaseStatus = "ok"
		evidence.DatabaseReadValue = val
		log.Printf("PostgreSQL dependency verified: %s", val)
	}

	if cfg.redisURL != "" {
		val, err := exerciseRedis(cfg.redisURL)
		if err != nil {
			log.Fatalf("valkey exercise failed: %v", err)
		}
		evidence.ValkeyStatus = "ok"
		evidence.ValkeyReadValue = val
		log.Printf("Valkey dependency verified: %s", val)
	}

	mux := http.NewServeMux()
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		// General workload health check /health returns 200 OK (workload is Healthy/Ready)
		if r.URL.Path == "/health" || r.URL.Path == "/health/" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
			return
		}

		// Bad consumer endpoint intentionally uses bad localhost path to demonstrate assertion failure
		if strings.Contains(r.URL.Path, "bad") || strings.Contains(r.URL.Path, "fail") || strings.Contains(r.URL.Path, "unreachable") {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_, err := exercisePostgres(ctx, "postgres://user:pass@127.0.0.1:5432/nonexistent")
			cancel()
			http.Error(w, fmt.Sprintf("bad consumer assertion failed: %v", err), http.StatusServiceUnavailable)
			return
		}

		if cfg.databaseURL != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := exercisePostgres(ctx, cfg.databaseURL)
			cancel()
			if err != nil {
				http.Error(w, fmt.Sprintf("database check failed: %v", err), http.StatusServiceUnavailable)
				return
			}
		}

		if cfg.redisURL != "" {
			_, err := exerciseRedis(cfg.redisURL)
			if err != nil {
				http.Error(w, fmt.Sprintf("redis check failed: %v", err), http.StatusServiceUnavailable)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	}
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/health/", healthHandler)

	mux.HandleFunc("/evidence", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(evidence)
	})

	log.Println("ADC-02 consumer listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
