package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

func newSyncCommand(configPath *string) *cobra.Command {
	var projectID string
	var sinceUnix int64
	var maxChunkBytes int32
	var statePath string
	var noState bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Stream telemetry sync chunks from Agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if projectID == "" {
				return fmt.Errorf("project-id is required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if statePath == "" {
				statePath = cfg.SyncStatePath
			}
			if !noState && sinceUnix == 0 && !cmd.Flags().Changed("since-unix") {
				stored, err := readSyncState(statePath, projectID)
				if err != nil {
					return err
				}
				sinceUnix = stored
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			req := &agentv1.SyncRequest{ProjectID: projectID, LastReceivedUnix: sinceUnix, MaxChunkBytes: maxChunkBytes}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			var maxEndUnix int64
			if err := agentclient.New(cfg).Sync(ctx, req, func(chunk *agentv1.SyncChunk) error {
				if chunk.EndUnix > maxEndUnix {
					maxEndUnix = chunk.EndUnix
				}
				return encoder.Encode(chunk)
			}); err != nil {
				return err
			}
			if !noState && maxEndUnix > 0 {
				return writeSyncState(statePath, projectID, maxEndUnix)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project-id", "", "project id to sync")
	cmd.Flags().Int64Var(&sinceUnix, "since-unix", 0, "last received unix timestamp")
	cmd.Flags().Int32Var(&maxChunkBytes, "max-chunk-bytes", 0, "max uncompressed chunk size")
	cmd.Flags().StringVar(&statePath, "state-path", "", "sync state file path")
	cmd.Flags().BoolVar(&noState, "no-state", false, "disable reading and updating sync state")
	return cmd
}

func readSyncState(path, projectID string) (int64, error) {
	path, err := resolveSyncStatePath(path)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read sync state: %w", err)
	}
	var state map[string]int64
	if err := json.Unmarshal(data, &state); err != nil {
		return 0, fmt.Errorf("parse sync state: %w", err)
	}
	return state[projectID], nil
}

func writeSyncState(path, projectID string, lastReceivedUnix int64) error {
	path, err := resolveSyncStatePath(path)
	if err != nil {
		return err
	}
	state := map[string]int64{}
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("parse sync state: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read sync state: %w", err)
	}
	state[projectID] = lastReceivedUnix
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sync state dir: %w", err)
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write sync state: %w", err)
	}
	return nil
}

func resolveSyncStatePath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve sync state path: %w", err)
	}
	return filepath.Join(dir, "opsi", "sync-state.json"), nil
}
