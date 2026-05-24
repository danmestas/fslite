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

// ignoreCmd reads/writes the running daemon's vfs-ignore-glob setting
// via /_admin/ignore. The setting is persisted in the fossil repo's
// config table so it survives across daemon restarts and travels with
// the repo on sync.
type ignoreCmd struct {
	Set   string `name:"set" help:"Set the ignore-glob (comma-separated patterns). Use \"-\" to disable filtering entirely."`
	Reset bool   `name:"reset" help:"Clear the persisted glob so built-in defaults take over."`
	Agent string `name:"agent" help:"Specific agent id (default: the singleton)."`
	URL   string `name:"url" help:"Override the daemon URL entirely (skips state lookup)."`
}

func (c *ignoreCmd) Run() error {
	url := c.URL
	if url == "" {
		s, err := loadLiveAgentState(c.Agent)
		if err != nil {
			return err
		}
		url = s.URL
	}

	switch {
	case c.Reset:
		return c.put(url, "")
	case c.Set != "":
		return c.put(url, c.Set)
	default:
		return c.get(url)
	}
}

func (c *ignoreCmd) get(url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/_admin/ignore")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ignore get: %d %s", resp.StatusCode, body)
	}
	parts := strings.SplitN(strings.TrimRight(string(body), "\n"), "\t", 2)
	source := parts[0]
	glob := ""
	if len(parts) == 2 {
		glob = parts[1]
	}
	fmt.Fprintf(os.Stdout, "source: %s\nglob:   %s\n", source, glob)
	return nil
}

func (c *ignoreCmd) put(url, glob string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url+"/_admin/ignore",
		bytes.NewReader([]byte(glob)))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ignore set: %d %s", resp.StatusCode, body)
	}
	fmt.Fprintf(os.Stdout, "%s", body)
	return nil
}
