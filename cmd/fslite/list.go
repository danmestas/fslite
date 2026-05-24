package main

import (
	"fmt"
	"os"
	"text/tabwriter"
)

// listCmd is a one-liner-per-daemon table of every recorded daemon.
// Same shape as `status --all` (status uses this under the hood).
type listCmd struct {
	JSON bool `name:"json" help:"Emit raw JSON instead of a human table."`
}

func (l *listCmd) Run() error {
	s := statusCmd{All: true, JSON: l.JSON}
	return s.runAll()
}

// silenceUnused keeps tabwriter / os reachable when only the JSON path
// is exercised in test builds.
var _ = tabwriter.NewWriter
var _ = os.Stdout
var _ = fmt.Sprintf
