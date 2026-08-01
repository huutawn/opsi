package bootstrapworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const (
	crashBarrierStep         = "install_k3s"
	crashBarrierBoundary     = "after_execute_before_checkpoint"
	crashBarrierStateVersion = 1
	crashBarrierArmed        = "armed"
	crashBarrierReached      = "reached"
	crashBarrierConsumed     = "consumed"
	crashBarrierCompleted    = "completed"
)

var barrierIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var barrierInstanceCounter atomic.Uint64

// StagingCrashBarrierConfig is intentionally specific to the install_k3s
// post-execute checkpoint boundary; it is not a generic fault-injection hook.
type StagingCrashBarrierConfig struct {
	Enabled     bool   `json:"enabled"`
	Environment string `json:"environment"`
	SessionID   string `json:"session_id"`
	RunID       string `json:"run_id"`
	Step        string `json:"step"`
	Boundary    string `json:"boundary"`
	StateDir    string `json:"state_dir"`
}

type crashBarrierState struct {
	Version     int    `json:"version"`
	State       string `json:"state"`
	Environment string `json:"environment"`
	SessionID   string `json:"session_id"`
	RunID       string `json:"run_id"`
	Step        string `json:"step"`
	Boundary    string `json:"boundary"`
	ProcessID   string `json:"process_id,omitempty"`
}

type stagingCrashBarrier struct {
	cfg       StagingCrashBarrierConfig
	processID string
}

func newStagingCrashBarrier(cfg StagingCrashBarrierConfig) stagingCrashBarrier {
	return stagingCrashBarrier{
		cfg:       cfg,
		processID: fmt.Sprintf("%d-%d", time.Now().UnixNano(), barrierInstanceCounter.Add(1)),
	}
}

func (c StagingCrashBarrierConfig) configured() bool {
	return c.Enabled || c.Environment != "" || c.SessionID != "" || c.RunID != "" || c.Step != "" || c.Boundary != "" || c.StateDir != ""
}

func (c StagingCrashBarrierConfig) validate(production bool) error {
	if !c.configured() {
		return nil
	}
	if production {
		return errors.New("staging_crash_barrier is not allowed in production")
	}
	if !c.Enabled {
		return errors.New("staging_crash_barrier fields require enabled=true")
	}
	if c.Environment != "staging" && c.Environment != "e2e" {
		return errors.New("staging_crash_barrier environment must be staging or e2e")
	}
	if c.SessionID == "" || !barrierIDPattern.MatchString(c.SessionID) {
		return errors.New("staging_crash_barrier session_id is invalid")
	}
	if c.RunID == "" || !barrierIDPattern.MatchString(c.RunID) {
		return errors.New("staging_crash_barrier run_id is invalid")
	}
	if c.Step != crashBarrierStep {
		return fmt.Errorf("staging_crash_barrier step must be %s", crashBarrierStep)
	}
	if c.Boundary != crashBarrierBoundary {
		return fmt.Errorf("staging_crash_barrier boundary must be %s", crashBarrierBoundary)
	}
	if !filepath.IsAbs(c.StateDir) {
		return errors.New("staging_crash_barrier state_dir must be absolute")
	}
	info, err := os.Lstat(c.StateDir)
	if err != nil {
		return fmt.Errorf("staging_crash_barrier state_dir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("staging_crash_barrier state_dir must be a private directory")
	}
	return nil
}

func (b stagingCrashBarrier) statePath() string {
	digest := sha256.Sum256([]byte(b.cfg.SessionID + "\x00" + b.cfg.RunID))
	return filepath.Join(b.cfg.StateDir, "install_k3s-"+hex.EncodeToString(digest[:16])+".json")
}

func (b stagingCrashBarrier) beforeCheckpoint(ctx context.Context, lease Lease, step BootstrapStep) error {
	if !b.cfg.Enabled || lease.Bundle.SessionID != b.cfg.SessionID || step.ID != crashBarrierStep {
		return nil
	}
	state, exists, err := readCrashBarrierState(b.statePath())
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := b.validateState(state); err != nil {
		return err
	}
	switch state.State {
	case crashBarrierArmed:
		if state.ProcessID != "" {
			return errors.New("staging_crash_barrier armed marker has a process ID")
		}
		state.State = crashBarrierReached
		state.ProcessID = b.processID
		if err := writeCrashBarrierState(b.statePath(), state); err != nil {
			return err
		}
		return waitForCrashBarrier(ctx)
	case crashBarrierReached:
		if state.ProcessID == "" {
			return errors.New("staging_crash_barrier reached marker has no process ID")
		}
		if state.ProcessID == b.processID {
			return waitForCrashBarrier(ctx)
		}
		state.State = crashBarrierConsumed
		state.ProcessID = b.processID
		return writeCrashBarrierState(b.statePath(), state)
	case crashBarrierConsumed, crashBarrierCompleted:
		if state.ProcessID == "" {
			return errors.New("staging_crash_barrier consumed marker has no process ID")
		}
		return nil
	default:
		return fmt.Errorf("staging_crash_barrier state %q is invalid", state.State)
	}
}

func (b stagingCrashBarrier) afterCheckpoint(lease Lease, step BootstrapStep) error {
	if !b.cfg.Enabled || lease.Bundle.SessionID != b.cfg.SessionID || step.ID != crashBarrierStep {
		return nil
	}
	state, exists, err := readCrashBarrierState(b.statePath())
	if err != nil || !exists {
		return err
	}
	if err := b.validateState(state); err != nil {
		return err
	}
	if state.State == crashBarrierConsumed {
		state.State = crashBarrierCompleted
		return writeCrashBarrierState(b.statePath(), state)
	}
	return nil
}

func (b stagingCrashBarrier) validateState(state crashBarrierState) error {
	if state.Version != crashBarrierStateVersion || state.Environment != b.cfg.Environment || state.SessionID != b.cfg.SessionID || state.RunID != b.cfg.RunID || state.Step != crashBarrierStep || state.Boundary != crashBarrierBoundary {
		return errors.New("staging_crash_barrier marker does not match configured target")
	}
	return nil
}

func waitForCrashBarrier(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func readCrashBarrierState(path string) (crashBarrierState, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return crashBarrierState{}, false, nil
	}
	if err != nil {
		return crashBarrierState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return crashBarrierState{}, false, errors.New("staging_crash_barrier marker must be a private regular file")
	}
	if info.Size() > 4096 {
		return crashBarrierState{}, false, errors.New("staging_crash_barrier marker is too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return crashBarrierState{}, false, err
	}
	defer file.Close()
	var state crashBarrierState
	decoder := json.NewDecoder(io.LimitReader(file, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return crashBarrierState{}, false, fmt.Errorf("decode staging_crash_barrier marker: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return crashBarrierState{}, false, errors.New("staging_crash_barrier marker contains trailing data")
	}
	return state, true, nil
}

func writeCrashBarrierState(path string, state crashBarrierState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".install_k3s-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func crashBarrierStateForConfig(cfg StagingCrashBarrierConfig, state, processID string) crashBarrierState {
	return crashBarrierState{Version: crashBarrierStateVersion, State: state, Environment: cfg.Environment, SessionID: cfg.SessionID, RunID: cfg.RunID, Step: crashBarrierStep, Boundary: crashBarrierBoundary, ProcessID: strings.TrimSpace(processID)}
}
