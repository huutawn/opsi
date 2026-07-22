package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	"github.com/spf13/cobra"
)

const deploymentCommandTimeout = 30 * time.Second

type deploymentSpecFlags struct {
	projectID      string
	buildRecordID  string
	environmentID  string
	serviceKey     string
	replicas       int32
	containerPort  int32
	cpuRequest     string
	memoryRequest  string
	cpuLimit       string
	memoryLimit    string
	termination    int64
	readinessPath  string
	livenessPath   string
	environment    []string
	secretRefs     []string
	exposure       string
	idempotencyKey string
	yes            bool
	jsonOutput     bool
}

type deploymentReadFlags struct {
	projectID      string
	deploymentID   string
	idempotencyKey string
	jsonOutput     bool
	yes            bool
}

func newDeployCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var legacy agentv1.DeployRequest
	var legacyRequests, legacyLimits, legacyDependencies []string
	root := &cobra.Command{Use: "deploy", Short: "Deploy an accepted BuildRecord through Cloud and the exact routed Agent", RunE: func(command *cobra.Command, _ []string) error {
		return runLegacyDevDeployment(command, *configPath, factory, &legacy, legacyRequests, legacyLimits, legacyDependencies)
	}}
	legacyFlags := root.Flags()
	legacyFlags.StringVar(&legacy.ProjectID, "project-id", "", "legacy development-only project scope; production uses a deploy subcommand")
	legacyFlags.StringVar(&legacy.ServiceID, "service-id", "", "legacy development-only service ID")
	legacyFlags.StringVar(&legacy.ServiceName, "service-name", "", "legacy development-only service name")
	legacyFlags.StringVar(&legacy.RepoURL, "repo-url", "", "legacy development-only Git repository")
	legacyFlags.StringVar(&legacy.Branch, "branch", "", "legacy development-only Git branch")
	legacyFlags.StringVar(&legacy.GitSHA, "git-sha", "", "legacy development-only Git commit")
	legacyFlags.StringVar(&legacy.ManifestPath, "manifest-path", "", "legacy development-only manifest path")
	legacyFlags.StringArrayVar(&legacy.WatchPaths, "watch-path", nil, "legacy development-only watch path")
	legacyFlags.StringArrayVar(&legacyDependencies, "depends-on", nil, "legacy development-only dependency")
	legacyFlags.Int32Var(&legacy.TerminationGracePeriodSeconds, "termination-grace-period-seconds", 0, "legacy development-only termination grace")
	legacyFlags.StringArrayVar(&legacyRequests, "resource-request", nil, "legacy development-only resource request key=value")
	legacyFlags.StringArrayVar(&legacyLimits, "resource-limit", nil, "legacy development-only resource limit key=value")
	for _, operation := range []string{"dry-run", "diff", "apply"} {
		flags := &deploymentSpecFlags{replicas: 1, containerPort: 8080, cpuRequest: "100m", memoryRequest: "128Mi", cpuLimit: "500m", memoryLimit: "512Mi", termination: 30, exposure: "internal"}
		operation := operation
		command := &cobra.Command{Use: operation, Short: deploymentOperationShort(operation), Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
			request, err := flags.request()
			if err != nil {
				return err
			}
			client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			switch operation {
			case "dry-run":
				preview, err := client.PreviewDeployment(ctx, flags.projectID, request)
				if err != nil {
					return err
				}
				return writeDeploymentPreview(command, preview, flags.jsonOutput)
			case "diff":
				preview, err := client.DiffDeployment(ctx, flags.projectID, request)
				if err != nil {
					return err
				}
				return writeDeploymentPreview(command, preview, flags.jsonOutput)
			default:
				if !flags.yes {
					return errors.New("deploy apply requires --yes")
				}
				if strings.TrimSpace(flags.idempotencyKey) == "" {
					return errors.New("idempotency-key is required for deploy apply")
				}
				job, err := client.ApplyDeployment(ctx, flags.projectID, flags.idempotencyKey, request)
				if err != nil {
					return err
				}
				return writeDeploymentJob(command, job, flags.jsonOutput)
			}
		}}
		bindDeploymentSpecFlags(command, flags, operation == "apply")
		root.AddCommand(command)
	}

	statusFlags := &deploymentReadFlags{}
	status := &cobra.Command{Use: "status", Short: "Show one durable deployment job", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, statusFlags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		if statusFlags.deploymentID == "" {
			return errors.New("deployment-id is required")
		}
		job, err := client.GetDeployment(ctx, statusFlags.projectID, statusFlags.deploymentID)
		if err != nil {
			return err
		}
		return writeDeploymentJob(command, job, statusFlags.jsonOutput)
	}}
	bindDeploymentReadFlags(status, statusFlags, true, false)
	root.AddCommand(status)

	listFlags := &deploymentReadFlags{}
	list := &cobra.Command{Use: "list", Short: "List durable deployment jobs", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, listFlags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		jobs, err := client.ListDeployments(ctx, listFlags.projectID)
		if err != nil {
			return err
		}
		if listFlags.jsonOutput {
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"deployments": jobs})
		}
		writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "JOB\tBUILD RECORD\tDIGEST\tRUNTIME\tNODE\tAGENT\tSTATE\tATTEMPT")
		for _, job := range jobs {
			buildRecordID, digest := "", ""
			if job.Snapshot != nil {
				buildRecordID, digest = job.Snapshot.Authority.BuildRecord.ID, job.Snapshot.Image.Digest
			}
			_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\n", job.ID, buildRecordID, digest, job.RuntimeID, job.NodeID, job.AgentID, job.Status, job.AttemptCount, job.MaxAttempts)
		}
		return writer.Flush()
	}}
	bindDeploymentReadFlags(list, listFlags, false, false)
	root.AddCommand(list)

	eventFlags := &deploymentReadFlags{}
	events := &cobra.Command{Use: "events", Short: "Show append-only deployment progress events", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, eventFlags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		if eventFlags.deploymentID == "" {
			return errors.New("deployment-id is required")
		}
		items, err := client.DeploymentEvents(ctx, eventFlags.projectID, eventFlags.deploymentID)
		if err != nil {
			return err
		}
		if eventFlags.jsonOutput {
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"events": items})
		}
		for _, event := range items {
			_, _ = fmt.Fprintf(command.OutOrStdout(), "%s  %3d%%  attempt=%d  %-14s  %s\n", event.CreatedAt.Format(time.RFC3339), event.ProgressPercent, event.Attempt, event.Step, event.MessageRedacted)
		}
		return nil
	}}
	bindDeploymentReadFlags(events, eventFlags, true, false)
	root.AddCommand(events)

	for _, operation := range []string{"cancel", "retry", "rollback"} {
		flags := &deploymentReadFlags{}
		operation := operation
		command := &cobra.Command{Use: operation, Short: deploymentOperationShort(operation), Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
			if flags.deploymentID == "" || flags.idempotencyKey == "" || !flags.yes {
				return errors.New("deployment-id, idempotency-key, and --yes are required")
			}
			client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			var job cloudclient.DeploymentJob
			if operation == "cancel" {
				job, err = client.CancelDeployment(ctx, flags.projectID, flags.deploymentID, flags.idempotencyKey)
			} else if operation == "rollback" {
				job, err = client.RollbackDeployment(ctx, flags.projectID, flags.deploymentID, flags.idempotencyKey)
			} else {
				job, err = client.RetryDeployment(ctx, flags.projectID, flags.deploymentID, flags.idempotencyKey)
			}
			if err != nil {
				return err
			}
			return writeDeploymentJob(command, job, flags.jsonOutput)
		}}
		bindDeploymentReadFlags(command, flags, true, true)
		root.AddCommand(command)
	}
	return root
}

func runLegacyDevDeployment(command *cobra.Command, configPath string, factory func() (keychain.Store, error), request *agentv1.DeployRequest, requests, limits, dependencies []string) error {
	if request.ProjectID == "" || request.ServiceID == "" || request.ServiceName == "" || request.RepoURL == "" || request.GitSHA == "" || request.ManifestPath == "" {
		return errors.New("choose a deploy subcommand for production; the direct Git path is development-only and requires project-id, service-id, service-name, repo-url, git-sha, and manifest-path")
	}
	var err error
	if request.ResourceRequestsJSON, err = encodeLegacyResourceFlags(requests); err != nil {
		return err
	}
	if request.ResourceLimitsJSON, err = encodeLegacyResourceFlags(limits); err != nil {
		return err
	}
	for _, value := range dependencies {
		if value = strings.TrimSpace(value); value != "" {
			request.DependsOn = append(request.DependsOn, agentv1.ServiceDependency{Name: value})
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(command.Context(), 15*time.Minute)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	encoder := json.NewEncoder(command.OutOrStdout())
	return agentclient.New(cfg).Deploy(ctx, request, func(event *agentv1.ProgressEvent) error { return encoder.Encode(event) })
}

func encodeLegacyResourceFlags(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	resources := map[string]string{}
	for _, value := range values {
		key, content, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(content) == "" {
			return "", fmt.Errorf("resource value must be key=value: %s", value)
		}
		resources[strings.TrimSpace(key)] = strings.TrimSpace(content)
	}
	data, err := json.Marshal(resources)
	return string(data), err
}

func deploymentOperationShort(operation string) string {
	switch operation {
	case "dry-run":
		return "Resolve topology, policy, route, digest, and WorkloadSpec without creating a job"
	case "diff":
		return "Compare the resolved immutable deployment with the latest successful job"
	case "apply":
		return "Create or reuse a durable immutable-image DeploymentJob"
	case "cancel":
		return "Cancel a job only before the Agent lease/runtime mutation boundary"
	default:
		return "Retry a lease-exhausted job in place without changing its job ID"
	}
}

func bindDeploymentSpecFlags(command *cobra.Command, flags *deploymentSpecFlags, mutation bool) {
	set := command.Flags()
	set.StringVar(&flags.projectID, "project-id", "", "project ID")
	set.StringVar(&flags.buildRecordID, "build-record-id", "", "accepted BuildRecord ID")
	set.StringVar(&flags.environmentID, "environment-id", "", "target environment ID; Cloud resolves runtime/node/Agent")
	set.StringVar(&flags.serviceKey, "service-key", "", "BuildRecord service key")
	set.Int32Var(&flags.replicas, "replicas", 1, "replicas matching the active TopologyPlan")
	set.Int32Var(&flags.containerPort, "container-port", 8080, "application container and ClusterIP Service port")
	set.StringVar(&flags.cpuRequest, "cpu-request", "100m", "CPU request matching the active TopologyPlan")
	set.StringVar(&flags.memoryRequest, "memory-request", "128Mi", "memory request matching the active TopologyPlan")
	set.StringVar(&flags.cpuLimit, "cpu-limit", "500m", "CPU limit")
	set.StringVar(&flags.memoryLimit, "memory-limit", "512Mi", "memory limit")
	set.Int64Var(&flags.termination, "termination-grace-seconds", 30, "termination grace period")
	set.StringVar(&flags.readinessPath, "readiness-path", "", "optional HTTP readiness path")
	set.StringVar(&flags.livenessPath, "liveness-path", "", "optional HTTP liveness path")
	set.StringArrayVar(&flags.environment, "env", nil, "non-secret environment NAME=value; repeatable")
	set.StringArrayVar(&flags.secretRefs, "secret-ref", nil, "opaque secret reference ENV_NAME=secret_id; unresolved references fail closed")
	set.StringVar(&flags.exposure, "exposure", "internal", "exposure intent: none or internal; R5-010 creates no external route")
	if mutation {
		set.StringVar(&flags.idempotencyKey, "idempotency-key", "", "idempotency identity for exact replay")
		set.BoolVar(&flags.yes, "yes", false, "confirm the deployment mutation")
	}
	set.BoolVar(&flags.jsonOutput, "json", false, "emit JSON")
}

func bindDeploymentReadFlags(command *cobra.Command, flags *deploymentReadFlags, deploymentID, mutation bool) {
	command.Flags().StringVar(&flags.projectID, "project-id", "", "project ID")
	if deploymentID {
		command.Flags().StringVar(&flags.deploymentID, "deployment-id", "", "deployment job ID")
	}
	if mutation {
		command.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "idempotency identity")
		command.Flags().BoolVar(&flags.yes, "yes", false, "confirm the deployment mutation")
	}
	command.Flags().BoolVar(&flags.jsonOutput, "json", false, "emit JSON")
}

func (f deploymentSpecFlags) request() (cloudclient.DeploymentCreateRequest, error) {
	if f.projectID == "" || f.buildRecordID == "" || f.environmentID == "" || f.serviceKey == "" {
		return cloudclient.DeploymentCreateRequest{}, errors.New("project-id, build-record-id, environment-id, and service-key are required")
	}
	environment, err := parseDeploymentPairs(f.environment)
	if err != nil {
		return cloudclient.DeploymentCreateRequest{}, err
	}
	secrets, err := parseSecretPairs(f.secretRefs)
	if err != nil {
		return cloudclient.DeploymentCreateRequest{}, err
	}
	spec := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: f.serviceKey, Replicas: f.replicas, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: f.containerPort, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: f.cpuRequest, Memory: f.memoryRequest}, Limits: deploymentv1.ResourceValues{CPU: f.cpuLimit, Memory: f.memoryLimit}}, TerminationGracePeriodSecond: f.termination, Environment: environment, SecretReferences: secrets, Exposure: deploymentv1.ExposureIntent{Mode: f.exposure}}
	if f.readinessPath != "" {
		spec.ReadinessProbe = defaultDeploymentProbe(f.readinessPath, f.containerPort)
	}
	if f.livenessPath != "" {
		spec.LivenessProbe = defaultDeploymentProbe(f.livenessPath, f.containerPort)
	}
	if err := spec.Validate(); err != nil {
		return cloudclient.DeploymentCreateRequest{}, err
	}
	return cloudclient.DeploymentCreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: f.buildRecordID, EnvironmentID: f.environmentID, Workload: spec}, nil
}

func parseDeploymentPairs(values []string) ([]deploymentv1.EnvironmentVariable, error) {
	result := make([]deploymentv1.EnvironmentVariable, 0, len(values))
	for _, value := range values {
		name, content, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("environment value must be NAME=value: %s", value)
		}
		result = append(result, deploymentv1.EnvironmentVariable{Name: strings.TrimSpace(name), Value: content})
	}
	return result, nil
}

func parseSecretPairs(values []string) ([]deploymentv1.SecretReference, error) {
	result := make([]deploymentv1.SecretReference, 0, len(values))
	for _, value := range values {
		name, secretID, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(secretID) == "" {
			return nil, fmt.Errorf("secret reference must be ENV_NAME=secret_id: %s", value)
		}
		result = append(result, deploymentv1.SecretReference{EnvName: strings.TrimSpace(name), SecretID: strings.TrimSpace(secretID)})
	}
	return result, nil
}

func defaultDeploymentProbe(path string, port int32) *deploymentv1.Probe {
	return &deploymentv1.Probe{Path: path, Port: port, InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 6}
}

func deploymentClient(parent context.Context, configPath string, factory func() (keychain.Store, error), projectID string) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	if err := validateGitHubProjectID(projectID); err != nil {
		return nil, nil, nil, err
	}
	client, err := newCommandCloudClient(configPath, Options{Version: "dev", KeychainFactory: factory})
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, deploymentCommandTimeout)
	return client, ctx, cancel, nil
}

func writeDeploymentPreview(command *cobra.Command, preview cloudclient.DeploymentPreview, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(command.OutOrStdout()).Encode(preview)
	}
	snapshot := preview.Snapshot
	_, err := fmt.Fprintf(command.OutOrStdout(), "Eligible: %t (%s)\nBuildRecord: %s\nImage: %s\nPlatform: %s\nTarget: runtime=%s node=%s agent=%s\nTopology: %s rev=%d\nPolicy: %s rev=%d\nSpec hash: %s\nChanges: %s\n", preview.Eligible, preview.DecisionCode, snapshot.Authority.BuildRecord.ID, snapshot.Image.Reference, snapshot.Authority.BuildRecord.Build.Platform, snapshot.Authority.RuntimeID, snapshot.Authority.NodeID, snapshot.Authority.AgentID, snapshot.Authority.TopologyPlanID, snapshot.Authority.TopologyRevision, snapshot.Authority.DeploymentPolicyID, snapshot.Authority.DeploymentPolicyRevision, snapshot.SpecHash, strings.Join(preview.Changes, ", "))
	return err
}

func writeDeploymentJob(command *cobra.Command, job cloudclient.DeploymentJob, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(command.OutOrStdout()).Encode(job)
	}
	buildRecordID, image, topology, policy := "", "", "", ""
	if job.Snapshot != nil {
		buildRecordID = job.Snapshot.Authority.BuildRecord.ID
		image = job.Snapshot.Image.Reference
		topology = fmt.Sprintf("%s rev=%d", job.Snapshot.Authority.TopologyPlanID, job.Snapshot.Authority.TopologyRevision)
		policy = fmt.Sprintf("%s rev=%d", job.Snapshot.Authority.DeploymentPolicyID, job.Snapshot.Authority.DeploymentPolicyRevision)
	}
	leaseExpiry, retryAfter := "", ""
	if job.LeaseExpiresAt != nil {
		leaseExpiry = job.LeaseExpiresAt.Format(time.RFC3339)
	}
	if job.RetryAfter != nil {
		retryAfter = job.RetryAfter.Format(time.RFC3339)
	}
	applicationImageID, resources, readiness := "", "", ""
	if job.TerminalResult != nil {
		applicationImageID = job.TerminalResult.ApplicationImageID
		resources = fmt.Sprintf("namespace=%s deployment=%s service=%s", job.TerminalResult.Namespace, job.TerminalResult.DeploymentName, job.TerminalResult.ServiceName)
		readiness = fmt.Sprintf("available_replicas=%d", job.TerminalResult.AvailableReplicas)
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "Job: %s\nState: %s\nReused: %t\nBuildRecord: %s\nImage: %s\nTarget: runtime=%s node=%s agent=%s\nTopology: %s\nPolicy: %s\nSpec hash: %s\nAttempt: %d/%d\nLease expires: %s\nRetry after: %s\nApplication imageID: %s\nKubernetes: %s\nReadiness: %s\nError: %s %s\n", job.ID, job.Status, job.Reused, buildRecordID, image, job.RuntimeID, job.NodeID, job.AgentID, topology, policy, job.SpecHash, job.AttemptCount, job.MaxAttempts, leaseExpiry, retryAfter, applicationImageID, resources, readiness, job.FailureCode, job.FailureMessageRedacted)
	return err
}
