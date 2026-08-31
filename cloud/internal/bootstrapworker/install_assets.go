package bootstrapworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type BootstrapPlan struct {
	Version string
	Steps   []BootstrapStep
}

type BootstrapStep struct {
	ID      string
	Status  string
	Message string
	Command CommandSpec
}

func ValidateBundle(bundle Bundle) error {
	if bundle.SessionID == "" || bundle.ProjectID == "" || bundle.NodeID == "" {
		return errors.New("bootstrap bundle is missing required ids")
	}
	if bundle.PublicHost == "" {
		return errors.New("public_host is required")
	}
	if bundle.SSHPort < 0 || bundle.SSHPort > 65535 {
		return errors.New("ssh_port is invalid")
	}
	if bundle.SSH.Username == "" {
		return errors.New("ssh username is required")
	}
	switch bundle.SSH.AuthMethod {
	case "command":
		if bundle.SSH.PrivateKey != "" || bundle.SSH.Password != "" {
			return errors.New("bootstrap command bundle contains SSH credentials")
		}
	case "password":
		if bundle.SSH.Password == "" || bundle.SSH.PrivateKey != "" {
			return errors.New("ssh password credential is missing")
		}
	case "private_key":
		if bundle.SSH.PrivateKey == "" || bundle.SSH.Password != "" {
			return errors.New("ssh private key credential is missing")
		}
	default:
		return errors.New("ssh auth_method is invalid")
	}
	if bundle.AgentRegistrationToken == "" {
		return errors.New("agent registration token is missing")
	}
	return nil
}

func BuildBootstrapPlan(cfg Config, bundle Bundle) (BootstrapPlan, error) {
	if bundle.Role != "" && bundle.Role != "first_server" {
		return BootstrapPlan{}, fmt.Errorf("%w: only first_server bootstrap is implemented", ErrRuntimeUnsupported)
	}
	if err := validateRemoteInstallConfig(cfg); err != nil {
		return BootstrapPlan{}, err
	}
	agentCloudURL := effectiveAgentCloudURL(cfg)
	env := map[string]string{
		"OPSI_AGENT_SHA256":                 cfg.AgentInstallSHA256,
		"OPSI_AGENT_URL":                    cfg.AgentInstallURL,
		"OPSI_CLOUD_URL":                    agentCloudURL,
		"OPSI_K3S_INSTALLER_SHA256":         cfg.K3sInstallerSHA256,
		"OPSI_K3S_INSTALLER_URL":            cfg.K3sInstallerURL,
		"OPSI_K3S_VERSION":                  strings.TrimSpace(cfg.K3sVersion),
		"OPSI_NODE_ID":                      bundle.NodeID,
		"OPSI_AGENT_PUBLIC_HOST":            bundle.PublicHost,
		"OPSI_PROJECT_ID":                   bundle.ProjectID,
		"OPSI_REGISTRATION_IDENTITY_SHA256": registrationIdentitySHA256(bundle.NodeID, bundle.ProjectID, agentCloudURL),
		"OPSI_REMOTE_USERNAME":              bundle.SSH.Username,
	}
	secretEnv := map[string]string{"OPSI_AGENT_REGISTRATION_TOKEN": bundle.AgentRegistrationToken}
	if bundle.Checkpoint.PlanVersion == registry.FirstServerBootstrapPlanVersionV2 {
		stepIDs := registry.FirstServerBootstrapPlanV2StepIDs()
		return BootstrapPlan{Version: registry.FirstServerBootstrapPlanVersionV2, Steps: []BootstrapStep{
			{ID: stepIDs[0], Status: "preflight", Message: "checking Ubuntu target prerequisites", Command: CommandSpec{Script: preflightScript, Env: env}},
			{ID: stepIDs[1], Status: "installing_k3s", Message: "installing verified K3s server", Command: CommandSpec{Script: installK3sScript, Env: env}},
			{ID: stepIDs[2], Status: "installing_agent", Message: "staging verified Opsi Agent release", Command: CommandSpec{Script: installAgentScript, Env: env}},
			{ID: stepIDs[3], Status: "registering_agent", Message: "registering and activating Opsi Agent", Command: CommandSpec{Script: registerAgentScript, Env: env, SensitiveEnv: secretEnv}},
		}}, nil
	}
	stepIDs := registry.FirstServerBootstrapPlanV3StepIDs()
	return BootstrapPlan{Version: registry.FirstServerBootstrapPlanVersionV3, Steps: []BootstrapStep{
		{ID: stepIDs[0], Status: "preflight", Message: "checking Ubuntu target prerequisites", Command: CommandSpec{Script: preflightScript, Env: env}},
		{ID: stepIDs[1], Status: "configure_swap", Message: "configuring idempotent system swap", Command: CommandSpec{Script: configureSwapScript, Env: env}},
		{ID: stepIDs[2], Status: "installing_k3s", Message: "installing verified K3s server", Command: CommandSpec{Script: installK3sScript, Env: env}},
		{ID: stepIDs[3], Status: "installing_agent", Message: "staging verified Opsi Agent release", Command: CommandSpec{Script: installAgentScript, Env: env}},
		{ID: stepIDs[4], Status: "registering_agent", Message: "registering and activating Opsi Agent", Command: CommandSpec{Script: registerAgentScript, Env: env, SensitiveEnv: secretEnv}},
	}}, nil
}

func validateRemoteInstallConfig(cfg Config) error {
	if err := validateK3sVersion(cfg.K3sVersion); err != nil {
		return fmt.Errorf("k3s_version: %w", err)
	}
	if err := validateRemoteURL(cfg.K3sInstallerURL, cfg.Production, "k3s_installer_url"); err != nil {
		return err
	}
	if err := validateRealSHA256(cfg.K3sInstallerSHA256); err != nil {
		return fmt.Errorf("k3s_installer_sha256: %w", err)
	}
	if err := validateRemoteURL(cfg.AgentInstallURL, cfg.Production, "agent_install_url"); err != nil {
		return err
	}
	if err := validateRealSHA256(cfg.AgentInstallSHA256); err != nil {
		return fmt.Errorf("agent_install_sha256: %w", err)
	}
	if err := validateRemoteURL(effectiveAgentCloudURL(cfg), cfg.Production, "agent_cloud_url"); err != nil {
		return err
	}
	return nil
}

func validateRemoteURL(raw string, production bool, name string) error {
	u, err := parseHTTPURL(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if production && u.Scheme != "https" {
		return fmt.Errorf("production requires https %s", name)
	}
	return nil
}

func effectiveAgentCloudURL(cfg Config) string {
	value := cfg.AgentCloudURL
	if value == "" {
		value = cfg.CloudURL
	}
	return strings.TrimRight(value, "/")
}

func registrationIdentitySHA256(nodeID, projectID, cloudURL string) string {
	payload, _ := json.Marshal(struct {
		NodeID    string `json:"node_id"`
		ProjectID string `json:"project_id"`
		CloudURL  string `json:"agent_cloud_url"`
	}{NodeID: nodeID, ProjectID: projectID, CloudURL: strings.TrimRight(cloudURL, "/")})
	return bootstrapSHA256Hex(payload)
}

func BootstrapPlanFingerprint(cfg Config, plan BootstrapPlan) string {
	type fingerprintStep struct {
		ID            string `json:"id"`
		CommandSHA256 string `json:"command_sha256"`
	}
	type fingerprintPayload struct {
		PlanVersion        string            `json:"plan_version"`
		Steps              []fingerprintStep `json:"steps"`
		K3sVersion         string            `json:"k3s_version"`
		K3sInstallerURL    string            `json:"k3s_installer_url"`
		K3sInstallerSHA256 string            `json:"k3s_installer_sha256"`
		AgentInstallURL    string            `json:"agent_install_url"`
		AgentInstallSHA256 string            `json:"agent_install_sha256"`
		AgentCloudURL      string            `json:"agent_cloud_url"`
		SystemdUnitSHA256  string            `json:"systemd_unit_sha256"`
	}
	steps := make([]fingerprintStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		command := step.Command
		command.SensitiveEnv = nil
		steps = append(steps, fingerprintStep{ID: step.ID, CommandSHA256: bootstrapSHA256Hex([]byte(renderScript(command)))})
	}
	payload, _ := json.Marshal(fingerprintPayload{
		PlanVersion:        plan.Version,
		Steps:              steps,
		K3sVersion:         strings.TrimSpace(cfg.K3sVersion),
		K3sInstallerURL:    cfg.K3sInstallerURL,
		K3sInstallerSHA256: cfg.K3sInstallerSHA256,
		AgentInstallURL:    cfg.AgentInstallURL,
		AgentInstallSHA256: cfg.AgentInstallSHA256,
		AgentCloudURL:      effectiveAgentCloudURL(cfg),
		SystemdUnitSHA256:  bootstrapSHA256Hex([]byte(opsiAgentSystemdUnit)),
	})
	return bootstrapSHA256Hex(payload)
}

func bootstrapSHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validateRealSHA256(raw string) error {
	if err := validateSHA256(raw); err != nil {
		return err
	}
	if raw == strings.Repeat("0", 64) {
		return errors.New("must not be the all-zero placeholder")
	}
	return nil
}

var (
	ErrBootstrapPlanMismatch      = errors.New("bootstrap plan mismatch")
	ErrBootstrapCheckpointInvalid = errors.New("bootstrap checkpoint invalid")
)

func ValidateBootstrapCheckpoint(plan BootstrapPlan, fingerprint string, checkpoint registry.BootstrapCheckpoint) error {
	if checkpoint.SchemaVersion != registry.BootstrapCheckpointSchemaVersion {
		return fmt.Errorf("%w: schema version", ErrBootstrapCheckpointInvalid)
	}
	if checkpoint.PlanVersion != plan.Version || checkpoint.PlanFingerprint != fingerprint {
		return ErrBootstrapPlanMismatch
	}
	if checkpoint.NextStepIndex < 0 || checkpoint.NextStepIndex > len(plan.Steps) {
		return fmt.Errorf("%w: next step index", ErrBootstrapCheckpointInvalid)
	}
	if checkpoint.NextStepIndex == 0 {
		if checkpoint.LastCompletedStep != "" {
			return fmt.Errorf("%w: completed step at index zero", ErrBootstrapCheckpointInvalid)
		}
		return nil
	}
	if checkpoint.LastCompletedStep != plan.Steps[checkpoint.NextStepIndex-1].ID {
		return fmt.Errorf("%w: completed step", ErrBootstrapCheckpointInvalid)
	}
	return nil
}

func classifyPlanFailure(err error) JobFailure {
	code := "AGENT_INSTALL_URL_INVALID"
	if errors.Is(err, ErrRuntimeUnsupported) {
		code = "BOOTSTRAP_ROLE_UNSUPPORTED"
	}
	return JobFailure{Code: code, Message: boundedFailureMessage(err.Error())}
}

func classifyCheckpointFailure(err error) JobFailure {
	if errors.Is(err, ErrBootstrapPlanMismatch) || cloudErrorCode(err) == "BOOTSTRAP_PLAN_MISMATCH" {
		return JobFailure{Code: "BOOTSTRAP_PLAN_MISMATCH", Message: "persisted bootstrap checkpoint belongs to a different plan", Retryable: false}
	}
	if errors.Is(err, ErrBootstrapCheckpointInvalid) || cloudErrorCode(err) == "BOOTSTRAP_CHECKPOINT_INVALID" {
		return JobFailure{Code: "BOOTSTRAP_CHECKPOINT_INVALID", Message: "persisted bootstrap checkpoint is invalid", Retryable: false}
	}
	return JobFailure{Code: "BOOTSTRAP_CLOUD_TEMPORARY", Message: "bootstrap checkpoint acknowledgement failed", Retryable: true}
}

func classifyConnectFailure(err error) JobFailure {
	message := boundedFailureMessage(err.Error())
	switch {
	case errors.Is(err, ErrSSHHostKeyVerificationRequired):
		return JobFailure{Code: "SSH_HOST_KEY_VERIFICATION_REQUIRED", Message: "SSH host-key verification requires operator-provided known_hosts", Retryable: false}
	case errors.Is(err, ErrSSHHostKeyVerificationFailed):
		return JobFailure{Code: "SSH_HOST_KEY_VERIFICATION_FAILED", Message: "SSH host-key verification failed", Retryable: false}
	case strings.Contains(strings.ToLower(message), "parse ssh private key"):
		return JobFailure{Code: "SSH_PRIVATE_KEY_INVALID", Message: boundedFailureMessage(message), Retryable: false}
	default:
		return JobFailure{Code: "BOOTSTRAP_CONNECT_FAILED", Message: boundedFailureMessage(message), Retryable: true}
	}
}

func classifyStepFailure(status, message string) JobFailure {
	for _, classified := range []JobFailure{
		{Code: "SWAP_FILE_CONFLICT", Retryable: false},
		{Code: "SWAP_INSUFFICIENT_DISK_SPACE", Retryable: false},
		{Code: "SWAP_CREATION_FAILED", Retryable: false},
		{Code: "SWAP_ACTIVATION_FAILED", Retryable: false},
		{Code: "SWAP_FSTAB_CONFLICT", Retryable: false},
		{Code: "SWAP_FSTAB_UPDATE_FAILED", Retryable: false},
		{Code: "K3S_INSTALLER_CHECKSUM_MISMATCH", Retryable: false},
		{Code: "K3S_VERSION_VERIFICATION_FAILED", Retryable: true},
		{Code: "AGENT_INSTALL_CHECKSUM_MISMATCH", Retryable: false},
		{Code: "AGENT_RELEASE_INTEGRITY_FAILED", Retryable: false},
		{Code: "AGENT_REGISTRATION_IDENTITY_MISMATCH", Retryable: false},
		{Code: "AGENT_REGISTRATION_STATE_INVALID", Retryable: false},
		{Code: "AGENT_TLS_GENERATION_FAILED", Retryable: false},
		{Code: "AGENT_SERVICE_START_FAILED", Retryable: true},
		{Code: "AGENT_ROLLBACK_FAILED", Retryable: false},
	} {
		if strings.Contains(message, classified.Code) {
			classified.Message = classified.Code
			return classified
		}
	}
	if status == "preflight" {
		return JobFailure{Code: "TARGET_OS_UNSUPPORTED", Message: boundedFailureMessage(message), Retryable: false}
	}
	if status == "configure_swap" {
		return JobFailure{Code: "SWAP_CONFIGURATION_FAILED", Message: boundedFailureMessage(message), Retryable: false}
	}
	return JobFailure{Code: "BOOTSTRAP_CLOUD_TEMPORARY", Message: boundedFailureMessage(message), Retryable: true}
}

func parseHTTPURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("must be an absolute HTTP(S) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("must use http or https")
	}
	if u.User != nil || u.Fragment != "" {
		return nil, errors.New("must not contain user info or a fragment")
	}
	return u, nil
}

func validateSHA256(raw string) error {
	if len(raw) != 64 {
		return errors.New("must contain exactly 64 hexadecimal characters")
	}
	if !sha256Pattern.MatchString(raw) {
		return errors.New("must be lowercase hexadecimal")
	}
	return nil
}

var (
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	k3sVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`)
)

func validateK3sVersion(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return errors.New("is required")
	}
	if !k3sVersionPattern.MatchString(value) {
		return errors.New("must match vX.Y.Z+k3sN without whitespace, slash, or shell metacharacters")
	}
	return nil
}

func isPlaceholderValue(raw string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	return strings.Contains(normalized, "REPLACE_WITH_") ||
		strings.Contains(normalized, "CHANGE_ME") ||
		strings.Contains(normalized, "EXAMPLE_SECRET")
}

const curlTransportFunctions = `
download_file() {
	url="$1"
	output="$2"
	case "$url" in
		https://*) curl --fail --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$output" "$url" ;;
		http://*) curl --fail --show-error --location --proto '=http,https' --proto-redir '=http,https' --output "$output" "$url" ;;
		*) return 2 ;;
	esac
}

post_json() {
	url="$1"
	payload="$2"
	output="$3"
	case "$url" in
		https://*) curl --fail --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --request POST --header 'content-type: application/json' --data-binary "@$payload" --output "$output" "$url" ;;
		http://*) curl --fail --show-error --location --proto '=http,https' --proto-redir '=http,https' --request POST --header 'content-type: application/json' --data-binary "@$payload" --output "$output" "$url" ;;
		*) return 2 ;;
	esac
}
`

const preflightScript = `
set -eu
. /etc/os-release
test "${ID:-}" = ubuntu
for command in curl systemctl sha256sum mktemp install readlink cmp stat openssl swapon mkswap df fallocate dd blkid grep tee sed awk cat chown mv rm; do command -v "$command" >/dev/null; done
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then command -v sudo >/dev/null && sudo -n true; fi
`

const configureSwapScript = `
set -eu
fail_code() { printf '%s\n' "$1" >&2; exit 1; }
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi

swap_dir="/var/lib/opsi"
swap_file="/var/lib/opsi/swapfile"
marker_file="/var/lib/opsi/swap.marker"
marker_value="opsi.swapfile/v1"

active_swap="$($SUDO swapon --show=NAME --noheadings 2>/dev/null || true)"
if [ -n "$active_swap" ] && ! printf '%s\n' "$active_swap" | grep -q "^${swap_file}$"; then
	exit 0
fi

validate_marker() {
	if ! $SUDO test -f "$marker_file" || $SUDO test -L "$marker_file"; then
		fail_code SWAP_FILE_CONFLICT
	fi
	marker_owner="$($SUDO stat -c '%u:%g' "$marker_file" 2>/dev/null || true)"
	[ "$marker_owner" = "0:0" ] || fail_code SWAP_FILE_CONFLICT
	marker_perm="$($SUDO stat -c '%a' "$marker_file" 2>/dev/null || true)"
	[ "$marker_perm" = "600" ] || fail_code SWAP_FILE_CONFLICT
	stored_marker="$($SUDO cat "$marker_file" 2>/dev/null || true)"
	[ "$stored_marker" = "$marker_value" ] || fail_code SWAP_FILE_CONFLICT
}

validate_swap_file() {
	if $SUDO test -L "$swap_file" || ! $SUDO test -f "$swap_file"; then
		fail_code SWAP_FILE_CONFLICT
	fi
	owner="$($SUDO stat -c '%u:%g' "$swap_file" 2>/dev/null || true)"
	[ "$owner" = "0:0" ] || fail_code SWAP_FILE_CONFLICT
	perm="$($SUDO stat -c '%a' "$swap_file" 2>/dev/null || true)"
	[ "$perm" = "600" ] || fail_code SWAP_FILE_CONFLICT
	size="$($SUDO stat -c '%s' "$swap_file" 2>/dev/null || true)"
	[ "$size" = "4294967296" ] || fail_code SWAP_FILE_CONFLICT
}

$SUDO install -d -m 0755 "$swap_dir"

if $SUDO test -e "$marker_file" || $SUDO test -L "$marker_file"; then
	validate_marker
elif $SUDO test -e "$swap_file" || $SUDO test -L "$swap_file"; then
	fail_code SWAP_FILE_CONFLICT
else
	tmp_marker="$swap_dir/.swap.marker.$$"
	$SUDO install -m 0600 /dev/null "$tmp_marker" || fail_code SWAP_CREATION_FAILED
	printf '%s\n' "$marker_value" | $SUDO tee "$tmp_marker" >/dev/null || fail_code SWAP_CREATION_FAILED
	$SUDO chown 0:0 "$tmp_marker" || fail_code SWAP_CREATION_FAILED
	$SUDO mv -T "$tmp_marker" "$marker_file" || fail_code SWAP_CREATION_FAILED
	validate_marker
fi

if $SUDO test -e "$swap_file" || $SUDO test -L "$swap_file"; then
	validate_swap_file
else
	avail_kb="$($SUDO df -k --output=avail "$swap_dir" 2>/dev/null | awk 'NR == 2 { print $1 }')"
	if [ -z "$avail_kb" ] || [ "$avail_kb" -lt 5242880 ]; then
		fail_code SWAP_INSUFFICIENT_DISK_SPACE
	fi

	tmp_swap="$swap_dir/.swapfile.$$"
	cleanup_swap_tmp() { $SUDO rm -f "$tmp_swap"; }
	trap cleanup_swap_tmp EXIT HUP INT TERM
	if ! $SUDO fallocate -l 4G "$tmp_swap" 2>/dev/null; then
		$SUDO rm -f "$tmp_swap"
		$SUDO dd if=/dev/zero of="$tmp_swap" bs=1M count=4096 status=none || fail_code SWAP_CREATION_FAILED
	fi

	$SUDO chmod 0600 "$tmp_swap" || fail_code SWAP_CREATION_FAILED
	$SUDO chown 0:0 "$tmp_swap" || fail_code SWAP_CREATION_FAILED
	tmp_size="$($SUDO stat -c '%s' "$tmp_swap" 2>/dev/null || true)"
	[ "$tmp_size" = "4294967296" ] || fail_code SWAP_CREATION_FAILED
	$SUDO mv -T "$tmp_swap" "$swap_file" || fail_code SWAP_CREATION_FAILED
	trap - EXIT HUP INT TERM
	validate_swap_file
fi

if ! $SUDO blkid "$swap_file" 2>/dev/null | grep -q 'TYPE="swap"'; then
	$SUDO mkswap "$swap_file" >/dev/null || fail_code SWAP_CREATION_FAILED
fi

fstab_line="${swap_file} none swap sw 0 0"
added_fstab=0
fstab_state="$($SUDO awk -v path="$swap_file" '
	$1 == path {
		count++
		if (NF != 6 || $2 != "none" || $3 != "swap" || $4 != "sw" || $5 != "0" || $6 != "0") bad=1
	}
	END { printf "%d:%d", count, bad }
' /etc/fstab 2>/dev/null)" || fail_code SWAP_FSTAB_UPDATE_FAILED
if [ "$fstab_state" = "0:0" ]; then
	printf '%s\n' "$fstab_line" | $SUDO tee -a /etc/fstab >/dev/null || fail_code SWAP_FSTAB_UPDATE_FAILED
	added_fstab=1
elif [ "$fstab_state" != "1:0" ]; then
	fail_code SWAP_FSTAB_CONFLICT
fi

if ! $SUDO swapon --show=NAME --noheadings 2>/dev/null | grep -q "^${swap_file}$"; then
	if ! $SUDO swapon "$swap_file"; then
		if [ "$added_fstab" -eq 1 ]; then
			$SUDO sed -i "/^[[:space:]]*\/var\/lib\/opsi\/swapfile[[:space:]]/d" /etc/fstab 2>/dev/null || true
		fi
		fail_code SWAP_ACTIVATION_FAILED
	fi
fi

if ! $SUDO swapon --show=NAME --noheadings 2>/dev/null | grep -q "^${swap_file}$"; then
	if [ "$added_fstab" -eq 1 ]; then
		$SUDO sed -i "/^[[:space:]]*\/var\/lib\/opsi\/swapfile[[:space:]]/d" /etc/fstab 2>/dev/null || true
	fi
	fail_code SWAP_ACTIVATION_FAILED
fi
`

const installK3sScript = `
set -eu
` + curlTransportFunctions + `
fail_code() { printf '%s\n' "$1" >&2; exit 1; }
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
installer="$tmpdir/install-k3s.sh"
current_version=""
if command -v k3s >/dev/null 2>&1; then
	current_output="$($SUDO k3s --version 2>/dev/null)" || fail_code K3S_VERSION_VERIFICATION_FAILED
	current_version="$(printf '%s\n' "$current_output" | awk 'NR == 1 { print $3 }')"
fi
needs_reconcile=0
if ! $SUDO grep -q 'fail-swap-on=false' /etc/systemd/system/k3s.service 2>/dev/null && ! $SUDO grep -q 'fail-swap-on=false' /etc/rancher/k3s/config.yaml 2>/dev/null; then
	needs_reconcile=1
fi
if [ "$current_version" != "$OPSI_K3S_VERSION" ] || ! $SUDO test -s /var/lib/rancher/k3s/server/cred/encryption-config.json || [ "$needs_reconcile" -eq 1 ]; then
	download_file "$OPSI_K3S_INSTALLER_URL" "$installer"
	if ! printf '%s  %s\n' "$OPSI_K3S_INSTALLER_SHA256" "$installer" | sha256sum --check -; then
		fail_code K3S_INSTALLER_CHECKSUM_MISMATCH
	fi
	chmod 0700 "$installer"
	if [ "${OPSI_REMOTE_USERNAME}" = root ]; then
		INSTALL_K3S_VERSION="$OPSI_K3S_VERSION" INSTALL_K3S_EXEC='server --write-kubeconfig-mode 0640 --secrets-encryption --kubelet-arg=fail-swap-on=false' sh "$installer"
	else
		sudo -n env INSTALL_K3S_VERSION="$OPSI_K3S_VERSION" INSTALL_K3S_EXEC='server --write-kubeconfig-mode 0640 --secrets-encryption --kubelet-arg=fail-swap-on=false' sh "$installer"
	fi
fi
$SUDO systemctl enable --now k3s
$SUDO systemctl is-active --quiet k3s
$SUDO test -s /var/lib/rancher/k3s/server/cred/encryption-config.json || fail_code K3S_SECRETS_ENCRYPTION_NOT_ENABLED
$SUDO k3s secrets-encrypt status | grep -q 'Encryption Status: Enabled' || fail_code K3S_SECRETS_ENCRYPTION_NOT_ENABLED
installed_output="$($SUDO k3s --version 2>/dev/null)" || fail_code K3S_VERSION_VERIFICATION_FAILED
installed_version="$(printf '%s\n' "$installed_output" | awk 'NR == 1 { print $3 }')"
[ "$installed_version" = "$OPSI_K3S_VERSION" ] || fail_code K3S_VERSION_VERIFICATION_FAILED
$SUDO grep -q 'fail-swap-on=false' /etc/systemd/system/k3s.service 2>/dev/null || $SUDO grep -q 'fail-swap-on=false' /etc/rancher/k3s/config.yaml 2>/dev/null || fail_code K3S_VERSION_VERIFICATION_FAILED
$SUDO k3s kubectl get nodes
`

const installAgentScript = `
set -eu
` + curlTransportFunctions + `
fail_code() { printf '%s\n' "$1" >&2; exit 1; }
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
release_root="/opt/opsi/agent/releases"
release="$release_root/$OPSI_AGENT_SHA256"
binary="$release/opsi-agent"
if $SUDO test -e "$release"; then
	$SUDO test -f "$binary" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
	actual="$($SUDO sha256sum "$binary" 2>/dev/null)" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
	[ "${actual%% *}" = "$OPSI_AGENT_SHA256" ] || fail_code AGENT_RELEASE_INTEGRITY_FAILED
	$SUDO "$binary" --version >/dev/null || fail_code AGENT_RELEASE_INTEGRITY_FAILED
	exit 0
fi
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
download="$tmpdir/opsi-agent"
download_file "$OPSI_AGENT_URL" "$download"
if ! printf '%s  %s\n' "$OPSI_AGENT_SHA256" "$download" | sha256sum --check -; then
	fail_code AGENT_INSTALL_CHECKSUM_MISMATCH
fi
$SUDO install -d -m 0755 "$release_root" "$release"
staged="$release/.opsi-agent.$$"
$SUDO install -m 0755 "$download" "$staged"
actual="$($SUDO sha256sum "$staged" 2>/dev/null)" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
[ "${actual%% *}" = "$OPSI_AGENT_SHA256" ] || fail_code AGENT_RELEASE_INTEGRITY_FAILED
$SUDO mv -f "$staged" "$binary"
actual="$($SUDO sha256sum "$binary" 2>/dev/null)" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
[ "${actual%% *}" = "$OPSI_AGENT_SHA256" ] || fail_code AGENT_RELEASE_INTEGRITY_FAILED
$SUDO "$binary" --version >/dev/null || fail_code AGENT_RELEASE_INTEGRITY_FAILED
`

const opsiAgentSystemdUnit = `[Unit]
Description=Opsi Agent
Documentation=https://github.com/opsi-dev/opsi
After=network-online.target k3s.service
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
ExecStart=/opt/opsi/agent/current/opsi-agent --config /etc/opsi/agent.yaml
Restart=always
RestartSec=5s
KillSignal=SIGTERM
TimeoutStopSec=30s
WorkingDirectory=/var/lib/opsi
StateDirectory=opsi
LogsDirectory=opsi
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=multi-user.target
`

const registerAgentScript = `
set -eu
` + curlTransportFunctions + `
fail_code() { printf '%s\n' "$1" >&2; exit 1; }
atomic_link() {
	target="$1"
	link="$2"
	tmp_link="${link}.tmp.$$"
	$SUDO rm -f "$tmp_link"
	$SUDO ln -s "$target" "$tmp_link"
	$SUDO mv -Tf "$tmp_link" "$link"
}
wait_agent_health() {
	attempt=0
	while [ "$attempt" -lt 30 ]; do
		if curl --fail --silent --show-error http://127.0.0.1:9080/health >/dev/null 2>&1 && $SUDO systemctl is-active --quiet opsi-agent; then
			return 0
		fi
		attempt=$((attempt + 1))
		sleep 1
	done
	return 1
}
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
release_root="/opt/opsi/agent/releases"
target_release="$release_root/$OPSI_AGENT_SHA256"
target_binary="$target_release/opsi-agent"
current_link="/opt/opsi/agent/current"
previous_link="/opt/opsi/agent/previous"
marker_dir="/var/lib/opsi/bootstrap"
marker="$marker_dir/registration.identity"
config="/etc/opsi/agent.yaml"
unit="/etc/systemd/system/opsi-agent.service"
$SUDO test -f "$target_binary" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
actual="$($SUDO sha256sum "$target_binary" 2>/dev/null)" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
[ "${actual%% *}" = "$OPSI_AGENT_SHA256" ] || fail_code AGENT_RELEASE_INTEGRITY_FAILED
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
umask 077
if $SUDO test -e "$marker"; then
	marker_metadata="$($SUDO stat -c '%u:%a' "$marker" 2>/dev/null)" || fail_code AGENT_REGISTRATION_STATE_INVALID
	[ "$marker_metadata" = "0:600" ] || fail_code AGENT_REGISTRATION_STATE_INVALID
	stored_identity="$($SUDO cat "$marker" 2>/dev/null)" || fail_code AGENT_REGISTRATION_STATE_INVALID
	[ "$stored_identity" = "$OPSI_REGISTRATION_IDENTITY_SHA256" ] || fail_code AGENT_REGISTRATION_IDENTITY_MISMATCH
	$SUDO test -f "$config" || fail_code AGENT_REGISTRATION_STATE_INVALID
	config_metadata="$($SUDO stat -c '%u:%a' "$config" 2>/dev/null)" || fail_code AGENT_REGISTRATION_STATE_INVALID
	[ "$config_metadata" = "0:600" ] || fail_code AGENT_REGISTRATION_STATE_INVALID
	$SUDO "$target_binary" --config "$config" --check >/dev/null 2>&1 || fail_code AGENT_REGISTRATION_STATE_INVALID
else
	payload="$tmpdir/register.json"
	response="$tmpdir/register.response"
	config_tmp="$tmpdir/agent.yaml"
	cert_tmp="$tmpdir/server.crt"
	key_tmp="$tmpdir/server.key"
	case "$OPSI_AGENT_PUBLIC_HOST" in
		*[!0-9.]* ) san="DNS:$OPSI_AGENT_PUBLIC_HOST" ;;
		* ) san="IP:$OPSI_AGENT_PUBLIC_HOST" ;;
	esac
	openssl req -x509 -newkey rsa:2048 -sha256 -nodes -keyout "$key_tmp" -out "$cert_tmp" -days 30 -subj "/CN=$OPSI_AGENT_PUBLIC_HOST" -addext "subjectAltName=$san" >/dev/null 2>&1 || fail_code AGENT_TLS_GENERATION_FAILED
	cert_sha256="$(openssl x509 -in "$cert_tmp" -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
	[ "${#cert_sha256}" = 64 ] || fail_code AGENT_TLS_GENERATION_FAILED
	printf '{"registration_token":"%s","public_key_fingerprint":"bootstrap-%s","version":"bootstrap","capabilities":{"deploy":true,"node_lifecycle":true},"agent_endpoint":"%s","agent_port":9443,"agent_tls_server_name":"%s","agent_cert_sha256":"%s"}' "$OPSI_AGENT_REGISTRATION_TOKEN" "$OPSI_NODE_ID" "$OPSI_AGENT_PUBLIC_HOST" "$OPSI_AGENT_PUBLIC_HOST" "$cert_sha256" >"$payload"
	post_json "${OPSI_CLOUD_URL}/v1/agents/register" "$payload" "$response"
	agent_token="$(sed -n 's/.*"agent_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$response")"
	[ -n "$agent_token" ] || fail_code AGENT_REGISTRATION_STATE_INVALID
	cat >"$config_tmp" <<EOF
node_id: ${OPSI_NODE_ID}
mode: dev
listen_addr: 0.0.0.0:9443
health_addr: 127.0.0.1:9080
cloud_endpoint: ${OPSI_CLOUD_URL}
sqlite_path: /var/lib/opsi/opsi-agent.sqlite
tls:
  server_cert_path: /etc/opsi/tls/server.crt
  server_key_path: /etc/opsi/tls/server.key
auth:
  enabled: true
  verify_cache_ttl: 15m
cloud_relay:
  enabled: true
  project_id: ${OPSI_PROJECT_ID}
  agent_token: ${agent_token}
  poll_interval: 2s
  long_poll_wait: 30s
  heartbeat_interval: 10s
  sign_requests: true
deployment:
  project_id: ${OPSI_PROJECT_ID}
telemetry:
  enabled: true
secret:
  namespace: default
  kubectl_path: kubectl
  totp_namespace: default
  encryption_at_rest_confirmed: true
EOF
	"$target_binary" --config "$config_tmp" --check >/dev/null 2>&1 || fail_code AGENT_REGISTRATION_STATE_INVALID
	$SUDO install -d -m 0750 /etc/opsi /var/lib/opsi
	$SUDO install -d -m 0700 /etc/opsi/tls
	cert_stage="/etc/opsi/tls/.server.crt.$$"
	key_stage="/etc/opsi/tls/.server.key.$$"
	$SUDO install -m 0644 "$cert_tmp" "$cert_stage"
	$SUDO install -m 0600 "$key_tmp" "$key_stage"
	$SUDO mv -f "$cert_stage" /etc/opsi/tls/server.crt
	$SUDO mv -f "$key_stage" /etc/opsi/tls/server.key
	config_stage="/etc/opsi/.agent.yaml.$$"
	$SUDO install -m 0600 "$config_tmp" "$config_stage"
	$SUDO mv -f "$config_stage" "$config"
	$SUDO install -d -m 0700 "$marker_dir"
	marker_tmp="$tmpdir/registration.identity"
	printf '%s\n' "$OPSI_REGISTRATION_IDENTITY_SHA256" >"$marker_tmp"
	marker_stage="$marker_dir/.registration.identity.$$"
	$SUDO install -m 0600 "$marker_tmp" "$marker_stage"
	$SUDO mv -f "$marker_stage" "$marker"
fi
unit_tmp="$tmpdir/opsi-agent.service"
cat >"$unit_tmp" <<'OPSI_SYSTEMD_UNIT'
` + opsiAgentSystemdUnit + `OPSI_SYSTEMD_UNIT
unit_changed=0
if ! $SUDO test -f "$unit" || ! $SUDO cmp -s "$unit_tmp" "$unit"; then
	unit_stage="/etc/systemd/system/.opsi-agent.service.$$"
	$SUDO install -m 0644 "$unit_tmp" "$unit_stage"
	$SUDO mv -f "$unit_stage" "$unit"
	unit_changed=1
fi
$SUDO install -d -m 0755 /opt/opsi/agent
if $SUDO test -L "$current_link"; then
	old_release="$($SUDO readlink -f "$current_link")" || fail_code AGENT_RELEASE_INTEGRITY_FAILED
	case "$old_release" in "$release_root"/*) ;; *) fail_code AGENT_RELEASE_INTEGRITY_FAILED ;; esac
	if [ "$old_release" != "$target_release" ]; then
		atomic_link "releases/${old_release##*/}" "$previous_link"
	fi
fi
atomic_link "releases/$OPSI_AGENT_SHA256" "$current_link"
if [ "$unit_changed" -eq 1 ]; then $SUDO systemctl daemon-reload; fi
$SUDO systemctl enable opsi-agent
if $SUDO systemctl restart opsi-agent && wait_agent_health; then
	exit 0
fi
if $SUDO test -L "$previous_link"; then
	previous_release="$($SUDO readlink -f "$previous_link")" || fail_code AGENT_ROLLBACK_FAILED
	case "$previous_release" in "$release_root"/*) ;; *) fail_code AGENT_ROLLBACK_FAILED ;; esac
	[ "$previous_release" != "$target_release" ] || fail_code AGENT_ROLLBACK_FAILED
	atomic_link "releases/${previous_release##*/}" "$current_link"
	if $SUDO systemctl restart opsi-agent && wait_agent_health; then
		fail_code AGENT_SERVICE_START_FAILED
	fi
	fail_code AGENT_ROLLBACK_FAILED
fi
fail_code AGENT_SERVICE_START_FAILED
`
