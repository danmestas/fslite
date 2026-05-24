package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
	"text/tabwriter"
	"time"
)

// statusCmd prints state for one daemon (the singleton by default; a
// specific one if --agent is set) and probes /healthz for liveness.
// With --all it lists every recorded daemon as a table.
type statusCmd struct {
	Agent string `name:"agent" help:"Specific agent id (default: the most recent / singleton)."`
	All   bool   `name:"all" help:"Show every recorded daemon as a table."`
	JSON  bool   `name:"json" help:"Emit raw JSON instead of a human summary."`
}

func (s *statusCmd) Run() error {
	if s.All {
		return s.runAll()
	}

	state, err := resolveState(s.Agent)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fslite:", err)
		return err
	}
	alive := processAlive(state.PID)
	healthy := false
	if alive {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(state.URL + "/healthz")
		if err == nil {
			healthy = resp.StatusCode == http.StatusOK
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	if s.JSON {
		out := struct {
			daemonState
			Alive   bool `json:"alive"`
			Healthy bool `json:"healthy"`
		}{*state, alive, healthy}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("pid          %d (%s)\n", state.PID, livenessLabel(alive, healthy))
	fmt.Printf("url          %s\n", state.URL)
	fmt.Printf("repo         %s\n", state.RepoPath)
	fmt.Printf("agent        %s\n", state.AgentID)
	if state.NATSURL != "" {
		fmt.Printf("mode         synced (NATS-mediated)\n")
		fmt.Printf("nats         %s\n", state.NATSURL)
		if state.ProjectCode != "" {
			fmt.Printf("project      %s\n", state.ProjectCode)
		}
	} else {
		fmt.Printf("mode         local-only (no NATS)\n")
	}
	if state.Mountpoint != "" {
		fmt.Printf("mountpoint   %s\n", state.Mountpoint)
	}
	fmt.Printf("started      %s\n", state.StartedAt.Format(time.RFC3339))
	return nil
}

// runAll prints a one-line-per-daemon table sourced from the agents/ dir.
func (s *statusCmd) runAll() error {
	states, err := listAgentStates()
	if err != nil {
		return err
	}
	if len(states) == 0 {
		fmt.Fprintln(os.Stderr, "fslite: no recorded daemons")
		return nil
	}

	if s.JSON {
		type row struct {
			daemonState
			Alive bool `json:"alive"`
		}
		out := make([]row, len(states))
		for i, st := range states {
			out[i] = row{*st, processAlive(st.PID)}
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tPID\tSTATUS\tURL\tREPO\tMODE\tMOUNT")
	for _, st := range states {
		alive := processAlive(st.PID)
		status := "stale"
		if alive {
			status = "alive"
		}
		mode := "local-only"
		if st.NATSURL != "" {
			mode = "synced"
		}
		mp := st.Mountpoint
		if mp == "" {
			mp = "-"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			st.AgentID, st.PID, status, st.URL, st.RepoPath, mode, mp)
	}
	return tw.Flush()
}

func livenessLabel(alive, healthy bool) string {
	switch {
	case !alive:
		return "stale; process gone"
	case !healthy:
		return "alive but unhealthy"
	default:
		return "alive + healthy"
	}
}

// stopCmd SIGTERMs a running daemon. With --all, stops every alive
// daemon recorded under agents/. With --agent <id>, stops that one.
// Without flags, stops the singleton (the most recent daemon).
type stopCmd struct {
	Agent string `name:"agent" help:"Specific agent id to stop (default: the singleton)."`
	All   bool   `name:"all" help:"Stop every alive daemon."`
}

func (s *stopCmd) Run() error {
	if s.All {
		return s.stopAll()
	}

	state, err := resolveState(s.Agent)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fslite stop:", err)
		return nil
	}
	return stopOne(state)
}

func (s *stopCmd) stopAll() error {
	states, err := listAgentStates()
	if err != nil {
		return err
	}
	if len(states) == 0 {
		fmt.Fprintln(os.Stderr, "fslite stop: no recorded daemons")
		return nil
	}
	for _, st := range states {
		_ = stopOne(st)
	}
	return nil
}

func stopOne(state *daemonState) error {
	if !processAlive(state.PID) {
		_ = removeState(state.AgentID)
		fmt.Fprintf(os.Stderr, "fslite stop: %s already gone; cleaned stale state\n", state.AgentID)
		return nil
	}
	p, err := os.FindProcess(state.PID)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM pid %d (%s): %w", state.PID, state.AgentID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(state.PID) {
			fmt.Fprintf(os.Stderr, "fslite stop: %s (pid %d) exited\n", state.AgentID, state.PID)
			_ = removeState(state.AgentID)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("agent %s pid %d still alive after 5s SIGTERM wait", state.AgentID, state.PID)
}
