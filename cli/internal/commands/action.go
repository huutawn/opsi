package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cli/internal/actionapproval"
	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var ErrActionSecureCleanupRequired = errors.New("ACTION_SECURE_CLEANUP_REQUIRED")

func newActionCommand(configPath *string, options Options) *cobra.Command {
	var projectID, nodeID, serviceID, environmentID, runtimeID, kind, deviceID, displayName, incidentID string
	var replicas int32
	action := &cobra.Command{Use: "action", Short: "Human-approved typed runtime actions"}
	device := &cobra.Command{Use: "device", Short: "Manage local approval devices"}
	register := &cobra.Command{Use: "register", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" || displayName == "" {
			return errors.New("project-id and display-name are required")
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		store, err := options.KeychainFactory()
		if err != nil {
			return err
		}
		secure := actionapproval.Store{Backend: store}
		publicKey, privateKey, err := actionapproval.GenerateDevice()
		if err != nil {
			return err
		}
		defer func() {
			for i := range privateKey {
				privateKey[i] = 0
			}
		}()
		pat, err := actionPAT(store)
		if err != nil {
			return err
		}
		cloud, err := cloudclient.New(cfg.CloudURL, pat, options.Version, options.HTTPClient)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		idempotency, err := randomActionID()
		if err != nil {
			return err
		}
		registered, err := cloud.RegisterActionDevice(ctx, projectID, displayName, idempotency, publicKey)
		if err != nil {
			return err
		}
		if err := secure.SavePrivateKey(registered.ID, privateKey); err != nil {
			_, _ = cloud.RevokeActionDevice(ctx, projectID, registered.ID)
			return errors.New("store device private key in OS secure store")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"device_id": registered.ID, "fingerprint_sha256": registered.FingerprintSHA256, "status": registered.Status})
	}}
	list := &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		store, err := options.KeychainFactory()
		if err != nil {
			return err
		}
		pat, err := actionPAT(store)
		if err != nil {
			return err
		}
		cloud, err := cloudclient.New(cfg.CloudURL, pat, options.Version, options.HTTPClient)
		if err != nil {
			return err
		}
		devices, err := cloud.ListActionDevices(cmd.Context(), projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"devices": devices})
	}}
	revoke := &cobra.Command{Use: "revoke <device-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		store, err := options.KeychainFactory()
		if err != nil {
			return err
		}
		pat, err := actionPAT(store)
		if err != nil {
			return err
		}
		cloud, err := cloudclient.New(cfg.CloudURL, pat, options.Version, options.HTTPClient)
		if err != nil {
			return err
		}
		device, err := cloud.RevokeActionDevice(cmd.Context(), projectID, args[0])
		if err != nil {
			return err
		}
		receipt := map[string]any{"device_id": device.ID, "status": device.Status}
		if err := (actionapproval.Store{Backend: store}).DeletePrivateKey(args[0]); err != nil {
			receipt["local_cleanup_required"] = true
			if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(receipt); encodeErr != nil {
				return encodeErr
			}
			return ErrActionSecureCleanupRequired
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	device.AddCommand(register, list, revoke)

	catalog := &cobra.Command{Use: "catalog", RunE: func(cmd *cobra.Command, _ []string) error {
		client, pat, err := newActionAgent(cmd, *configPath, options.KeychainFactory)
		if err != nil {
			return err
		}
		response, err := client.ActionCatalog(agentclient.WithPAT(cmd.Context(), pat))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
	}}
	preflight := &cobra.Command{Use: "preflight", RunE: func(cmd *cobra.Command, _ []string) error {
		request, err := actionRequest(projectID, nodeID, serviceID, environmentID, runtimeID, kind, replicas, incidentID)
		if err != nil {
			return err
		}
		client, pat, err := newActionAgent(cmd, *configPath, options.KeychainFactory)
		if err != nil {
			return err
		}
		response, err := client.ActionPreflight(agentclient.WithPAT(cmd.Context(), pat), request)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
	}}
	approve := &cobra.Command{Use: "approve <challenge-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if projectID == "" || deviceID == "" {
			return errors.New("project-id and device-id are required")
		}
		if !interactiveInput(cmd.InOrStdin(), options) {
			return errors.New("interactive TTY approval is required")
		}
		client, pat, err := newActionAgent(cmd, *configPath, options.KeychainFactory)
		if err != nil {
			return err
		}
		challenge, err := client.ActionChallenge(agentclient.WithPAT(cmd.Context(), pat), &actionv1.ChallengeRequest{ProjectID: projectID, ChallengeID: args[0]})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "action=%s risk=%s state_hash=%s expires=%s\n", challenge.ActionID, challenge.Risk, challenge.StateHash, challenge.ExpiresAt.UTC().Format(time.RFC3339))
		fmt.Fprintln(cmd.OutOrStdout(), formatApprovalTarget(challenge.Target))
		fmt.Fprintf(cmd.OutOrStdout(), "summary=%s\n", challenge.Summary)
		for _, condition := range challenge.Preconditions {
			fmt.Fprintf(cmd.OutOrStdout(), "precondition=%s: %s\n", condition.Code, condition.Summary)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Type %s to approve: ", challenge.ConfirmationPhrase)
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(line) != challenge.ConfirmationPhrase {
			return errors.New("confirmation phrase did not match")
		}
		store, err := options.KeychainFactory()
		if err != nil {
			return err
		}
		secure := actionapproval.Store{Backend: store}
		privateKey, err := secure.PrivateKey(deviceID)
		if err != nil {
			return errors.New("device private key is unavailable in OS secure store")
		}
		defer func() {
			for i := range privateKey {
				privateKey[i] = 0
			}
		}()
		grant, err := actionapproval.Sign(*challenge, deviceID, privateKey)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(grant)
		if err != nil {
			return err
		}
		if err := secure.SavePendingGrant(challenge.ID, encoded); err != nil {
			return errors.New("store pending approval in OS secure store")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"challenge_id": challenge.ID, "device_id": deviceID, "expires_at": challenge.ExpiresAt})
	}}
	execute := &cobra.Command{Use: "execute <challenge-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		client, pat, err := newActionAgent(cmd, *configPath, options.KeychainFactory)
		if err != nil {
			return err
		}
		store, err := options.KeychainFactory()
		if err != nil {
			return err
		}
		secure := actionapproval.Store{Backend: store}
		encoded, err := secure.PendingGrant(args[0])
		if err != nil {
			return errors.New("pending approval is unavailable in OS secure store")
		}
		var grant actionv1.ApprovalGrant
		if err := actionv1.DecodeStrict(encoded, &grant); err != nil {
			return errors.New("pending approval is invalid")
		}
		result, err := client.ActionExecute(agentclient.WithPAT(cmd.Context(), pat), &actionv1.ExecuteRequest{ProjectID: projectID, ChallengeID: args[0], Grant: grant})
		if err != nil {
			return err
		}
		if !result.Status.Terminal() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		if err := secure.DeletePendingGrant(args[0]); err != nil {
			receipt := struct {
				Status                actionv1.ActionStatus `json:"status"`
				ActionID              string                `json:"action_id"`
				ChallengeID           string                `json:"challenge_id"`
				SecureCleanupRequired bool                  `json:"secure_cleanup_required"`
			}{Status: result.Status, ActionID: result.ActionID, ChallengeID: result.ChallengeID, SecureCleanupRequired: true}
			if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(receipt); encodeErr != nil {
				return encodeErr
			}
			return ErrActionSecureCleanupRequired
		}
		receipt := struct {
			actionv1.ActionResult
			SecureCleanupRequired bool `json:"secure_cleanup_required"`
		}{ActionResult: *result}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	status := &cobra.Command{Use: "status <action-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		client, pat, err := newActionAgent(cmd, *configPath, options.KeychainFactory)
		if err != nil {
			return err
		}
		result, err := client.ActionStatus(agentclient.WithPAT(cmd.Context(), pat), &actionv1.StatusRequest{ProjectID: projectID, ActionID: args[0]})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	for _, command := range []*cobra.Command{register, list, revoke, preflight, approve, execute, status} {
		command.Flags().StringVar(&projectID, "project-id", "", "project id")
	}
	register.Flags().StringVar(&displayName, "display-name", "", "bounded device display name")
	approve.Flags().StringVar(&deviceID, "device-id", "", "registered approval device id")
	preflight.Flags().StringVar(&nodeID, "node-id", "", "target node id")
	preflight.Flags().StringVar(&serviceID, "service-id", "", "target service id")
	preflight.Flags().StringVar(&environmentID, "environment-id", "", "target environment id")
	preflight.Flags().StringVar(&runtimeID, "runtime-id", "", "target runtime id")
	preflight.Flags().StringVar(&kind, "kind", "", "typed action kind")
	preflight.Flags().Int32Var(&replicas, "replicas", -1, "typed replica target")
	preflight.Flags().StringVar(&incidentID, "incident-id", "", "incident id")
	action.AddCommand(device, catalog, preflight, approve, execute, status)
	return action
}

func formatApprovalTarget(target actionv1.TargetIdentity) string {
	return fmt.Sprintf("target: project=%s environment=%s runtime=%s node=%s service=%s", humanApprovalField(target.ProjectID), humanApprovalField(target.EnvironmentID), humanApprovalField(target.RuntimeID), humanApprovalField(target.NodeID), humanApprovalField(target.ServiceID))
}

func humanApprovalField(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return string(runes)
}

func newActionAgent(cmd *cobra.Command, configPath string, factory func() (keychain.Store, error)) (*agentclient.Client, string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, "", err
	}
	store, err := factory()
	if err != nil {
		return nil, "", err
	}
	pat, err := actionPAT(store)
	if err != nil {
		return nil, "", err
	}
	return agentclient.New(cfg), pat, nil
}

func actionPAT(store keychain.Store) (string, error) {
	pat, err := store.GetPAT()
	if err != nil || strings.TrimSpace(pat) == "" {
		return "", errors.New("PAT is required in OS keychain")
	}
	return pat, nil
}
func actionRequest(projectID, nodeID, serviceID, environmentID, runtimeID, kind string, replicas int32, incidentID string) (*actionv1.PreflightRequest, error) {
	request := &actionv1.PreflightRequest{SchemaVersion: actionv1.SchemaVersion, ProjectID: projectID, NodeID: nodeID, ServiceID: serviceID, Target: actionv1.TargetIdentity{ProjectID: projectID, NodeID: nodeID, ServiceID: serviceID, EnvironmentID: environmentID, RuntimeID: runtimeID}, Kind: actionv1.ActionKind(kind)}
	switch request.Kind {
	case actionv1.ActionRestartWorkload:
		request.Parameters.RestartWorkload = &actionv1.RestartWorkloadParameters{}
	case actionv1.ActionScaleWorkload:
		if replicas < 0 {
			return nil, errors.New("replicas is required for scale_workload")
		}
		request.Parameters.ScaleWorkload = &actionv1.ScaleWorkloadParameters{Replicas: replicas}
	case actionv1.ActionGatewayReconcile:
		request.Parameters.GatewayReconcile = &actionv1.GatewayReconcileParameters{}
	case actionv1.ActionIncidentResolve:
		request.Parameters.IncidentResolve = &actionv1.IncidentResolveParameters{IncidentID: incidentID}
	default:
		return nil, errors.New("kind is not in the ActionPlane catalog")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return request, nil
}
func interactiveInput(reader io.Reader, options Options) bool {
	if options.IsTerminal != nil {
		return options.IsTerminal(reader)
	}
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
func randomActionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate action device idempotency identity")
	}
	return hex.EncodeToString(value[:]), nil
}
