package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/opsi-dev/opsi/agent/internal/dr"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: opsi-agent-dr backup|restore [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dir string
	var paths dr.StatePaths
	fs.StringVar(&dir, "dir", "", "backup staging directory")
	fs.StringVar(&paths.DeployDB, "deploy-db", "", "Agent deploy SQLite path")
	fs.StringVar(&paths.ServiceCatalogDB, "service-catalog-db", "", "Agent service catalog SQLite path")
	fs.StringVar(&paths.TelemetryDB, "telemetry-db", "", "Agent telemetry SQLite path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "backup":
		return dr.BackupAgentState(context.Background(), dir, paths)
	case "restore":
		return dr.RestoreAgentState(context.Background(), dir, paths)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
