package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/adminbootstrap"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/webhookrelay"
)

func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "bootstrap-owner" {
		fmt.Fprintln(stderr, "usage: opsi-cloud admin bootstrap-owner [flags]")
		return 2
	}
	return runBootstrapOwner(args[1:], stdout, stderr)
}

func runBootstrapOwner(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("opsi-cloud admin bootstrap-owner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "cloud JSON config path (required)")
	email := fs.String("email", "", "initial owner email (required)")
	orgName := fs.String("org-name", "", "organization name (required)")
	orgSlug := fs.String("org-slug", "", "organization slug")
	projectName := fs.String("project-name", "", "project name (required)")
	projectSlug := fs.String("project-slug", "", "project slug")
	displayName := fs.String("display-name", "", "owner display name")
	oauthProvider := fs.String("oauth-provider", "", "configured OAuth provider")
	oauthSubject := fs.String("oauth-subject", "", "verified provider subject to prelink")
	linkExistingOwner := fs.Bool("link-existing-owner", false, "link OAuth identity to the canonical initialized owner")
	patOutputFile := fs.String("pat-output-file", "", "new mode-0600 file for the initial PAT")
	jsonOutput := fs.Bool("json", false, "write metadata as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: opsi-cloud admin bootstrap-owner --config FILE [--link-existing-owner --oauth-provider PROVIDER --oauth-subject SUBJECT | --email EMAIL --org-name NAME --project-name NAME (--oauth-provider PROVIDER --oauth-subject SUBJECT | --pat-output-file FILE)] [flags]")
		fmt.Fprintln(stderr, "Flags: --config --email --org-name --org-slug --project-name --project-slug --display-name --oauth-provider --oauth-subject --link-existing-owner --pat-output-file --json")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments")
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "ADMIN_BOOTSTRAP_CONFLICT: config is required")
		return 2
	}
	if !*linkExistingOwner && (*email == "" || *orgName == "" || *projectName == "") {
		fmt.Fprintln(stderr, "ADMIN_BOOTSTRAP_CONFLICT: email, org-name, and project-name are required")
		return 2
	}
	cfg, err := webhookrelay.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	req, err := adminbootstrap.NormalizeAndValidate(adminbootstrap.Request{
		Email: *email, DisplayName: *displayName, OrgName: *orgName, OrgSlug: *orgSlug,
		ProjectName: *projectName, ProjectSlug: *projectSlug, OAuthProvider: *oauthProvider,
		OAuthSubject: *oauthSubject, LinkExistingOwner: *linkExistingOwner, IssuePAT: *patOutputFile != "",
	}, "github")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, adminbootstrap.CodeRequiresPostgres+": bootstrap-owner requires database_url")
		return 1
	}

	var output *patOutput
	if req.IssuePAT {
		output, err = preparePATOutput(*patOutputFile)
		if err != nil {
			fmt.Fprintln(stderr, adminbootstrap.CodePATOutputUnavailable+": "+err.Error())
			return 1
		}
		defer output.cleanup()
		if !output.existed {
			raw, hash, _, err := auth.NewPAT(90*24*time.Hour, time.Now().UTC())
			if err != nil {
				fmt.Fprintln(stderr, adminbootstrap.CodePATOutputUnavailable+": generate initial PAT")
				return 1
			}
			if err := output.write(raw); err != nil {
				fmt.Fprintln(stderr, adminbootstrap.CodePATOutputUnavailable+": "+err.Error())
				return 1
			}
			req.PATTokenHash = hash
			raw = ""
		}
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "open postgres:", err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(stderr, "connect postgres:", err)
		return 1
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		fmt.Fprintln(stderr, "migrate postgres:", err)
		return 1
	}
	result, err := (adminbootstrap.Service{DB: db}).ProvisionBootstrapOwner(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if output != nil {
		if result.PATCreated {
			if err := output.finalize(); err != nil {
				fmt.Fprintln(stderr, adminbootstrap.CodePATOutputUnavailable+": database committed but PAT file finalization failed; rotate the bootstrap-owner PAT through the normal PAT lifecycle")
				return 1
			}
		} else if result.InitialPATUnavailable {
			fmt.Fprintln(stderr, "initial PAT already exists; secret is not recoverable and no new PAT was issued")
		}
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "write JSON output:", err)
			return 1
		}
		return 0
	}
	writeBootstrapOwnerResult(stdout, result, *patOutputFile)
	return 0
}

func writeBootstrapOwnerResult(w io.Writer, result adminbootstrap.Result, patPath string) {
	fmt.Fprintln(w, "Bootstrap owner ready")
	fmt.Fprintln(w, "User ID:", result.UserID)
	fmt.Fprintln(w, "Organization ID:", result.OrganizationID)
	fmt.Fprintln(w, "Project ID:", result.ProjectID)
	fmt.Fprintln(w, "Role:", result.MembershipRole)
	fmt.Fprintln(w, "OAuth linked:", result.OAuthLinked)
	fmt.Fprintln(w, "Initial PAT created:", result.PATCreated)
	if result.PATCreated {
		fmt.Fprintln(w, "PAT output file:", patPath)
	}
	fmt.Fprintln(w, "Reused:", result.Reused)
}
