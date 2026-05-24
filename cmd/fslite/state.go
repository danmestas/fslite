package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// daemonState is what `fslite serve` writes to disk so that sibling
// subcommands (commit, mount, status, stop, ignore) can find the
// running instance without env-var coordination.
//
// Layout (in ~/.fslite/):
//
//	state.json                  pointer to the "most recent" daemon;
//	                            read by subcommands that don't take
//	                            --agent (single-daemon UX, unchanged).
//	agents/<agent-id>.json      one file per running daemon. Read by
//	                            `fslite status --agent <id>`, etc.
//
// `fslite serve` writes BOTH on start, removes BOTH on graceful exit
// (the singleton pointer is only cleared if it still points at us —
// other daemons keep it for themselves).
type daemonState struct {
	PID         int       `json:"pid"`
	HTTPAddr    string    `json:"http_addr"`
	URL         string    `json:"url"`
	RepoPath    string    `json:"repo_path"`
	AgentID     string    `json:"agent_id"`
	ProjectCode string    `json:"project_code,omitempty"`
	NATSURL     string    `json:"nats_url,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	// Mountpoint, when non-empty, records that `fslite mount` mounted
	// the running daemon at that path. `fslite unmount` reads it.
	Mountpoint string `json:"mountpoint,omitempty"`
}

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fslite"), nil
}

func statePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func agentStatePath(agentID string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agents", agentID+".json"), nil
}

// writeState persists both the per-agent file and the singleton pointer
// (last-wins). serve calls this on start.
func writeState(s daemonState) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	ap, _ := agentStatePath(s.AgentID)
	if err := os.WriteFile(ap, data, 0o600); err != nil {
		return err
	}
	sp, _ := statePath()
	return os.WriteFile(sp, data, 0o600)
}

// readState returns the singleton ("most recent") daemon's state.
func readState() (*daemonState, error) {
	sp, err := statePath()
	if err != nil {
		return nil, err
	}
	return readStateFromPath(sp, "no running fslite daemon (no "+sp+")")
}

// loadAgentState reads ~/.fslite/agents/<id>.json.
func loadAgentState(agentID string) (*daemonState, error) {
	ap, err := agentStatePath(agentID)
	if err != nil {
		return nil, err
	}
	return readStateFromPath(ap, fmt.Sprintf("no daemon recorded with agent id %q", agentID))
}

func readStateFromPath(path, missingMsg string) (*daemonState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if missingMsg == "" {
				return nil, nil
			}
			return nil, fmt.Errorf("%s", missingMsg)
		}
		return nil, err
	}
	var s daemonState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// listAgentStates returns every recorded daemon (alive or stale; caller
// filters via processAlive). Sorted by AgentID.
func listAgentStates() ([]*daemonState, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "agents"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*daemonState
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, "agents", e.Name())
		s, err := readStateFromPath(path, "")
		if err == nil && s != nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

// removeState drops the per-agent file and clears the singleton pointer
// if it still points at this agent. serve calls this on graceful exit.
func removeState(agentID string) error {
	ap, _ := agentStatePath(agentID)
	if err := os.Remove(ap); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s, err := readState(); err == nil && s.AgentID == agentID {
		sp, _ := statePath()
		_ = os.Remove(sp)
	}
	return nil
}

// processAlive returns true when a process with the given PID exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// resolveState is the standard "find the daemon a subcommand should
// talk to" routine. Empty agentID → singleton; non-empty → specific.
func resolveState(agentID string) (*daemonState, error) {
	if agentID == "" {
		return readState()
	}
	return loadAgentState(agentID)
}

// loadLiveState returns the singleton daemon's state if alive; cleans
// up stale state file otherwise.
func loadLiveState() (*daemonState, error) {
	s, err := readState()
	if err != nil {
		return nil, err
	}
	if !processAlive(s.PID) {
		_ = removeState(s.AgentID)
		return nil, fmt.Errorf("stale state file: pid %d is not running (cleaned up)", s.PID)
	}
	return s, nil
}

// loadLiveAgentState is loadLiveState for a specific agent id; if
// agentID is empty it falls through to the singleton.
func loadLiveAgentState(agentID string) (*daemonState, error) {
	if agentID == "" {
		return loadLiveState()
	}
	s, err := loadAgentState(agentID)
	if err != nil {
		return nil, err
	}
	if !processAlive(s.PID) {
		_ = removeState(agentID)
		return nil, fmt.Errorf("stale state for agent %q: pid %d is not running (cleaned up)", agentID, s.PID)
	}
	return s, nil
}
