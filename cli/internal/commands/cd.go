package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

type cdPlanOptions struct {
	RepoDir    string
	ConfigPath string
	Base       string
	Head       string
	Event      string
	JSON       bool
}

func newCDCommand(options Options) *cobra.Command {
	root := &cobra.Command{Use: "cd", Short: "Inspect repository CD intent without building or deploying"}
	planOptions := cdPlanOptions{RepoDir: ".", ConfigPath: defaultConfigPath, Event: string(repository.EventPush)}
	plan := &cobra.Command{Use: "plan", Short: "Resolve affected monorepo services", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDPlan(cmd.OutOrStdout(), options, planOptions, cmd)
	}}
	flags := plan.Flags()
	flags.StringVar(&planOptions.RepoDir, "repo-dir", ".", "local Git repository directory")
	flags.StringVar(&planOptions.ConfigPath, "config-path", defaultConfigPath, "repository-relative Opsi CD config path")
	flags.StringVar(&planOptions.Base, "base", "", "full base commit ID")
	flags.StringVar(&planOptions.Head, "head", "", "full head commit ID")
	flags.StringVar(&planOptions.Event, "event", string(repository.EventPush), "event type: initial, push, pull_request, or merge")
	flags.BoolVar(&planOptions.JSON, "json", false, "print the versioned plan as JSON")
	root.AddCommand(plan)
	return root
}

func runCDPlan(output io.Writer, options Options, input cdPlanOptions, cmd *cobra.Command) error {
	root, err := repository.Root(cmd.Context(), options.GitRunner, input.RepoDir)
	if err != nil {
		return err
	}
	cfg, _, _, err := repository.LoadConfig(root, input.ConfigPath)
	if err != nil {
		return err
	}
	plan, err := (repository.CDService{Runner: options.GitRunner}).Plan(cmd.Context(), repository.PlanRequest{Event: repository.EventType(input.Event), Base: strings.TrimSpace(input.Base), Head: strings.TrimSpace(input.Head), Repository: root, Config: cfg})
	if err != nil {
		return err
	}
	if input.JSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(plan)
	}
	return writeHumanPlan(output, plan)
}

func writeHumanPlan(output io.Writer, plan repository.ChangedServicePlan) error {
	if _, err := fmt.Fprintf(output, "Opsi CD plan %s\nEvent: %s\nBase: %s\nHead: %s\nFull build: %t\nAffected services: %s\n", plan.SchemaVersion, plan.Event, displayRevision(plan.Base), displayRevision(plan.Head), plan.FullBuild, displayServices(plan.AffectedServiceKeys)); err != nil {
		return err
	}
	for _, service := range plan.Services {
		for _, reason := range service.Reasons {
			if _, err := fmt.Fprintf(output, "- %s [%s]: %s\n", service.Key, reason.Code, reason.Explanation); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(output, "Explanation: %s\nConfig hash: %s\nPlan hash: %s\n", plan.Explanation, plan.ConfigHash, plan.PlanHash)
	return err
}

func displayRevision(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}
func displayServices(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
