package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const runStateSchemaVersion = 1

type runState struct {
	SchemaVersion int          `json:"schema_version"`
	LastRun       time.Time    `json:"last_run"`
	LastSource    string       `json:"last_source,omitempty"`
	LastResults   []warmResult `json:"last_results"`
	Logs          []logEntry   `json:"logs"`
}

func defaultStateFile() string {
	dir, errConfig := os.UserConfigDir()
	if errConfig != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "CLIProxyAPI", pluginID, "history.json")
}

// Configuration may select a JSON filename within one of the two trusted state roots.
// The deployment operator must protect those roots and their parents from untrusted writes.
const maxStateBytes = 16 << 20

func secureStatePath(path string) (string, error) {
	if path == "" {
		path = defaultStateFile()
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid state file path")
	}
	name := filepath.Base(absolute)
	if strings.HasPrefix(name, ".") || len(name) > 128 || !strings.EqualFold(filepath.Ext(name), ".json") {
		return "", fmt.Errorf("state_file must be a JSON filename (maximum 128 bytes)")
	}
	for _, dir := range []string{filepath.Dir(defaultStateFile()), filepath.Join("plugins", "state")} {
		root, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, absolute)
		if err == nil && rel == name {
			return absolute, nil
		}
	}
	return "", fmt.Errorf("state_file must be directly inside the plugin state directory")
}

func openStateRoot(path string, create bool) (*os.Root, string, error) {
	path, err := secureStatePath(path)
	if err != nil {
		return nil, "", err
	}
	dir, name := filepath.Dir(path), filepath.Base(path)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	info, err := root.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, "", err
	}
	if err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, "", fmt.Errorf("state file must be a regular file, not a symlink")
	}
	return root, name, nil
}

func loadRunState(path string) (runState, bool, error) {
	root, name, err := openStateRoot(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return runState{}, false, nil
	}
	if err != nil {
		return runState{}, false, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return runState{}, false, nil
	}
	if err != nil {
		return runState{}, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return runState{}, false, err
	}
	if len(raw) > maxStateBytes {
		return runState{}, false, fmt.Errorf("state file exceeds size limit")
	}
	var state runState
	if err := json.Unmarshal(raw, &state); err != nil {
		return runState{}, false, fmt.Errorf("invalid warm-up history")
	}
	if state.SchemaVersion != runStateSchemaVersion {
		return runState{}, false, fmt.Errorf("unsupported warm-up history schema")
	}
	if len(state.LastResults) > maxCredentials || len(state.Logs) > 2000 {
		return runState{}, false, fmt.Errorf("state entry count exceeds safe limit")
	}
	return sanitizedState(state), true, nil
}

func writeRunState(path string, state runState) error {
	state = sanitizedState(state)
	state.SchemaVersion = runStateSchemaVersion
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > maxStateBytes {
		return fmt.Errorf("state file exceeds size limit")
	}
	root, name, err := openStateRoot(path, true)
	if err != nil {
		return err
	}
	defer root.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := fmt.Sprintf(".quota-warmer-%x.tmp", nonce)
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temp, name)
}

func sanitizedState(state runState) runState {
	results := make([]warmResult, len(state.LastResults))
	for i, result := range state.LastResults {
		result.AuthID, result.Label = "", ""
		if result.CredentialNumber == 0 {
			result.CredentialNumber = i + 1
		}
		if result.Error != "" {
			result.Error = "Warm-up request failed"
		}
		results[i] = result
	}
	logs := make([]logEntry, len(state.Logs))
	for i, entry := range state.Logs {
		entry.Fields = sanitizeLogFields(entry.Fields)
		logs[i] = entry
	}
	state.LastResults, state.Logs = results, logs
	if strings.HasPrefix(state.LastSource, "schedule:") {
		state.LastSource = "schedule"
	}
	return state
}

func (w *warmer) persistLocked() error {
	if w.stateFile == "" {
		return nil
	}
	if w.stateLocked {
		return fmt.Errorf("warm-up state is read-only: %s", w.stateError)
	}
	errWrite := writeRunState(w.stateFile, runState{
		LastRun: w.lastRun, LastSource: w.lastSource, LastResults: w.lastResults, Logs: w.logs,
	})
	if errWrite != nil {
		w.stateError = "Unable to save warm-up history"
	} else {
		w.stateError = ""
	}
	return errWrite
}

func (w *warmer) logStateFailure(_ string) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{
		"level":   "error",
		"message": "Codex warm-up state persistence failed",
		"fields":  map[string]any{"plugin_id": pluginID},
	})
}
