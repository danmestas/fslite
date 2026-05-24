package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// commitCmd POSTs to /_admin/commit on the running daemon, draining any
// uncommitted overlay writes into a new Fossil check-in. The check-in's
// RID/UUID is printed.
type commitCmd struct {
	Message []string `arg:"" optional:"" help:"Commit message (joined by spaces)."`
	Agent   string   `name:"agent" help:"Specific agent id (default: the singleton)."`
	URL     string   `name:"url" help:"Override the daemon URL entirely (skips state lookup)."`
}

func (c *commitCmd) Run() error {
	url := c.URL
	if url == "" {
		s, err := loadLiveAgentState(c.Agent)
		if err != nil {
			return err
		}
		url = s.URL
	}

	msg := strings.TrimSpace(strings.Join(c.Message, " "))
	if msg == "" {
		msg = "fslite cli commit"
	}

	req, err := http.NewRequest(http.MethodPost, url+"/_admin/commit", bytes.NewReader([]byte(msg)))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("commit POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("commit: status %d: %s", resp.StatusCode, body)
	}
	fmt.Fprintf(os.Stdout, "%s", body)
	return nil
}
