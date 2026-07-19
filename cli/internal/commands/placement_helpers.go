package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
)

const placementCommandTimeout = 30 * time.Second

func placementClient(parent context.Context, configPath string, options Options, projectID string) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	if err := validateGitHubProjectID(projectID); err != nil {
		return nil, nil, nil, err
	}
	client, err := newCommandCloudClient(configPath, options)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create placement Cloud client: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, placementCommandTimeout)
	return client, ctx, cancel, nil
}

func readPlacementJSON(path string, dst any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("--file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open placement file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode strict placement JSON: %w", err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("placement file must contain exactly one JSON value")
	}
	return nil
}

func requirePlacementMutation(yes bool, key string) error {
	if !yes {
		return errors.New("mutation requires explicit --yes confirmation")
	}
	key = strings.TrimSpace(key)
	if len(key) < 8 || len(key) > 128 {
		return errors.New("--idempotency-key must contain 8-128 safe characters")
	}
	for _, r := range key {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return errors.New("--idempotency-key contains unsupported characters")
		}
	}
	return nil
}

func writePlacementJSON(writer io.Writer, value any) error {
	return json.NewEncoder(writer).Encode(value)
}
