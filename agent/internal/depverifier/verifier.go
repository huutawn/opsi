package depverifier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type Verifier struct {
	Runner      deploy.CommandRunner
	KubectlPath string
	Timeout     time.Duration
}

func (v Verifier) Verify(ctx context.Context, lease cloudrelay.DepVerificationLease) cloudrelay.DepVerificationResult {
	timeout := v.Timeout
	if lease.TimeoutSeconds > 0 {
		timeout = time.Duration(lease.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	vCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res := cloudrelay.DepVerificationResult{
		ID:                   lease.ID,
		LeaseToken:           lease.LeaseToken,
		ConnectionStatus:     verificationv1.LayerStatusNotSupported,
		ConsumerHealthStatus: verificationv1.LayerStatusUnhealthy,
		AssertionStatus:      verificationv1.LayerStatusNotConfigured,
	}

	// 1. Connection probe from consumer network context
	switch lease.ProviderKind {
	case "postgres":
		status, lat, failCode, msg := v.executePostgresProbe(vCtx, lease)
		res.ConnectionStatus = status
		res.ConnectionLatencyMs = lat
		res.ConnectionFailCode = failCode
		res.ConnectionMessage = msg
	case "valkey", "redis":
		status, lat, failCode, msg := v.executeValkeyProbe(vCtx, lease)
		res.ConnectionStatus = status
		res.ConnectionLatencyMs = lat
		res.ConnectionFailCode = failCode
		res.ConnectionMessage = msg
	case "application":
		status, lat, failCode, msg := v.executeAppDependencyProbe(vCtx, lease)
		res.ConnectionStatus = status
		res.ConnectionLatencyMs = lat
		res.ConnectionFailCode = failCode
		res.ConnectionMessage = msg
	default:
		res.ConnectionStatus = verificationv1.LayerStatusNotSupported
	}

	// 2. Consumer health check
	healthStatus, ready, total := v.checkConsumerHealth(vCtx, lease)
	res.ConsumerHealthStatus = healthStatus
	res.ConsumerReadyPods = ready
	res.ConsumerTotalPods = total

	// 3. Consumer assertion (if configured)
	if lease.AssertionPath != "" && lease.AssertionExpectedCode > 0 {
		aStatus, aCode, aFailCode, aMsg := v.executeConsumerAssertion(vCtx, lease)
		res.AssertionStatus = aStatus
		res.AssertionStatusCode = aCode
		res.AssertionFailCode = aFailCode
		res.AssertionMessage = aMsg
	}

	return res
}

func (v Verifier) executePostgresProbe(ctx context.Context, lease cloudrelay.DepVerificationLease) (status string, latencyMs int64, failCode, msg string) {
	consumerNS := lease.ConsumerNamespace
	if consumerNS == "" {
		consumerNS = "opsi-apps"
	}
	providerNS := lease.ProviderNamespace
	if providerNS == "" {
		providerNS = "opsi-managed"
	}

	targetHost := lease.BindingHost
	if targetHost == "" {
		targetHost = fmt.Sprintf("%s.%s.svc.cluster.local", lease.ProviderServiceName, providerNS)
	}
	targetPort := lease.BindingPort
	if targetPort <= 0 {
		targetPort = 5432
	}
	database := lease.BindingDatabase
	if database == "" {
		database = "opsi"
	}
	username := lease.BindingUsername
	if username == "" {
		username = "opsi"
	}
	password := lease.BindingPassword

	// If credentials were not supplied directly on the lease, attempt to resolve from provider secret
	if password == "" {
		if secJSON, secErr := v.run(ctx, nil, "get", "secret", "mrsec-"+lease.ProviderServiceName, "-n", providerNS, "-o", "json"); secErr == nil {
			var secObj struct {
				Data map[string]string `json:"data"`
			}
			if json.Unmarshal([]byte(secJSON), &secObj) == nil {
				if u, ok := secObj.Data["username"]; ok {
					if dec, err := base64.StdEncoding.DecodeString(u); err == nil && len(dec) > 0 {
						username = string(dec)
					}
				}
				if p, ok := secObj.Data["password"]; ok {
					if dec, err := base64.StdEncoding.DecodeString(p); err == nil {
						password = string(dec)
					}
				}
				if d, ok := secObj.Data["database"]; ok {
					if dec, err := base64.StdEncoding.DecodeString(d); err == nil && len(dec) > 0 {
						database = string(dec)
					}
				}
			}
		}
	}

	start := time.Now()
	probePodName := fmt.Sprintf("opsi-pg-probe-%s", sanitizeResourceName(lease.ID))
	defer v.cleanupProbePod(context.Background(), consumerNS, probePodName)

	script := `set -eu
IFS= read -r PGPASSWORD
export PGPASSWORD
out=$(psql -v ON_ERROR_STOP=1 -qAt -h "$1" -p "$2" -U "$3" -d "$4" -c 'BEGIN; CREATE TEMP TABLE opsi_dependency_probe(value text) ON COMMIT DROP; INSERT INTO opsi_dependency_probe(value) VALUES ('\''opsi-write-read'\''); SELECT value FROM opsi_dependency_probe; ROLLBACK;')
test "$out" = "opsi-write-read"
`
	input := []byte(password + "\n")
	_, err := v.run(ctx, input, "run", probePodName, "-n", consumerNS,
		"--image="+resourcev1.PostgresImage,
		"--labels=opsi.dev/probe=dep-verification,opsi.dev/lease-id="+sanitizeResourceName(lease.ID),
		"--restart=Never", "--rm", "-i", "--", "sh", "-ec", script, "pg-probe", targetHost, strconv.Itoa(targetPort), username, database)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return verificationv1.LayerStatusFailed, latency, verificationv1.FailureConnectionFailed, "PostgreSQL protocol verification failed"
	}
	return verificationv1.LayerStatusVerified, latency, "", "PostgreSQL connect and transactional write/read verified from consumer network context"
}

func (v Verifier) executeValkeyProbe(ctx context.Context, lease cloudrelay.DepVerificationLease) (status string, latencyMs int64, failCode, msg string) {
	consumerNS := lease.ConsumerNamespace
	if consumerNS == "" {
		consumerNS = "opsi-apps"
	}
	providerNS := lease.ProviderNamespace
	if providerNS == "" {
		providerNS = "opsi-managed"
	}

	targetHost := lease.BindingHost
	if targetHost == "" {
		targetHost = fmt.Sprintf("%s.%s.svc.cluster.local", lease.ProviderServiceName, providerNS)
	}
	targetPort := lease.BindingPort
	if targetPort <= 0 {
		targetPort = 6379
	}
	username := lease.BindingUsername
	password := lease.BindingPassword

	if password == "" && username == "" {
		if secJSON, secErr := v.run(ctx, nil, "get", "secret", "mrsec-"+lease.ProviderServiceName, "-n", providerNS, "-o", "json"); secErr == nil {
			var secObj struct {
				Data map[string]string `json:"data"`
			}
			if json.Unmarshal([]byte(secJSON), &secObj) == nil {
				if u, ok := secObj.Data["username"]; ok {
					if dec, err := base64.StdEncoding.DecodeString(u); err == nil && len(dec) > 0 {
						username = string(dec)
					}
				}
				if p, ok := secObj.Data["password"]; ok {
					if dec, err := base64.StdEncoding.DecodeString(p); err == nil {
						password = string(dec)
					}
				}
			}
		}
	}

	start := time.Now()
	probePodName := fmt.Sprintf("opsi-vk-probe-%s", sanitizeResourceName(lease.ID))
	defer v.cleanupProbePod(context.Background(), consumerNS, probePodName)

	script := `set -eu
IFS= read -r pass
if [ -n "$pass" ]; then export REDISCLI_AUTH="$pass"; fi
cleanup() {
  if [ -n "$3" ]; then
    valkey-cli -h "$1" -p "$2" --user "$3" DEL "$4" >/dev/null 2>&1 || true
  else
    valkey-cli -h "$1" -p "$2" DEL "$4" >/dev/null 2>&1 || true
  fi
}
trap 'cleanup "$1" "$2" "$3" "$4"' EXIT
if [ -n "$3" ]; then
  pong=$(valkey-cli -h "$1" -p "$2" --user "$3" PING)
  stored=$(valkey-cli -h "$1" -p "$2" --user "$3" SET "$4" opsi-write-read EX 30 NX)
  loaded=$(valkey-cli -h "$1" -p "$2" --user "$3" GET "$4")
else
  pong=$(valkey-cli -h "$1" -p "$2" PING)
  stored=$(valkey-cli -h "$1" -p "$2" SET "$4" opsi-write-read EX 30 NX)
  loaded=$(valkey-cli -h "$1" -p "$2" GET "$4")
fi
test "$pong" = "PONG"
test "$stored" = "OK"
test "$loaded" = "opsi-write-read"
`
	input := []byte(password + "\n")
	probeKey := "opsi:dependency-probe:" + sanitizeResourceName(lease.ID)
	_, err := v.run(ctx, input, "run", probePodName, "-n", consumerNS,
		"--image="+resourcev1.ValkeyImage,
		"--labels=opsi.dev/probe=dep-verification,opsi.dev/lease-id="+sanitizeResourceName(lease.ID),
		"--restart=Never", "--rm", "-i", "--", "sh", "-ec", script, "vk-probe", targetHost, strconv.Itoa(targetPort), username, probeKey)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return verificationv1.LayerStatusFailed, latency, verificationv1.FailureConnectionFailed, "Valkey connection probe failed"
	}
	return verificationv1.LayerStatusVerified, latency, "", "Valkey PING and ephemeral SET/GET with cleanup verified from consumer network context"
}

func (v Verifier) executeAppDependencyProbe(ctx context.Context, lease cloudrelay.DepVerificationLease) (status string, latencyMs int64, failCode, msg string) {
	consumerNS := lease.ConsumerNamespace
	if consumerNS == "" {
		consumerNS = "opsi-apps"
	}
	targetHost := lease.ProviderServiceName
	if !strings.Contains(targetHost, ".") {
		targetHost = fmt.Sprintf("%s.%s.svc.cluster.local", lease.ProviderServiceName, lease.ProviderNamespace)
	}

	start := time.Now()
	probePodName := fmt.Sprintf("opsi-app-probe-%s", sanitizeResourceName(lease.ID))
	defer v.cleanupProbePod(context.Background(), consumerNS, probePodName)

	script := fmt.Sprintf("nc -z -w 5 %s 80", targetHost)
	_, err := v.run(ctx, nil, "run", probePodName, "-n", consumerNS,
		"--image=busybox:1.36.1",
		"--labels=opsi.dev/probe=dep-verification,opsi.dev/lease-id="+sanitizeResourceName(lease.ID),
		"--restart=Never", "--rm", "-i", "--", "sh", "-c", script)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return verificationv1.LayerStatusFailed, latency, verificationv1.FailureConnectionFailed, "application dependency network probe failed"
	}
	return verificationv1.LayerStatusVerified, latency, "", "application dependency connectivity verified"
}

func (v Verifier) checkConsumerHealth(ctx context.Context, lease cloudrelay.DepVerificationLease) (status string, ready, total int) {
	ns := lease.ConsumerNamespace
	if ns == "" {
		ns = "opsi-apps"
	}
	out, err := v.run(ctx, nil, "get", "pods", "-n", ns, "-l", "opsi.dev/service="+lease.ConsumerServiceKey, "-o", "jsonpath={range .items[*]}{.status.containerStatuses[0].ready}{'\\n'}{end}")
	if err != nil {
		return verificationv1.LayerStatusUnhealthy, 0, 0
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	readyCount := 0
	totalCount := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		totalCount++
		if trimmed == "true" {
			readyCount++
		}
	}
	if totalCount > 0 && readyCount == totalCount {
		return verificationv1.LayerStatusHealthy, readyCount, totalCount
	}
	return verificationv1.LayerStatusUnhealthy, readyCount, totalCount
}

func (v Verifier) executeConsumerAssertion(ctx context.Context, lease cloudrelay.DepVerificationLease) (status string, code int, failCode, msg string) {
	if !strings.HasPrefix(lease.AssertionPath, "/") || strings.HasPrefix(lease.AssertionPath, "//") || strings.Contains(lease.AssertionPath, "..") || strings.Contains(lease.AssertionPath, "://") {
		return verificationv1.LayerStatusFailed, 0, verificationv1.FailureConsumerAssertionFailed, "assertion path must be a relative path starting with a single /"
	}
	ns := lease.ConsumerNamespace
	if ns == "" {
		ns = "opsi-apps"
	}
	port := lease.ConsumerInternalPort
	if port <= 0 {
		port = 8080
	}
	host := lease.ConsumerInternalHost
	if host == "" {
		host = lease.ConsumerServiceKey + "." + ns + ".svc.cluster.local"
	}

	targetURL := fmt.Sprintf("http://%s:%d%s", host, port, lease.AssertionPath)
	parsedURL, err := url.Parse(targetURL)
	if err != nil || parsedURL.Scheme != "http" || parsedURL.User != nil {
		return verificationv1.LayerStatusFailed, 0, verificationv1.FailureConsumerAssertionFailed, "assertion target URL is invalid"
	}

	// Platform-owned ephemeral curl probe in consumer namespace (strict NO-REDIRECT to prevent SSRF)
	probePodName := fmt.Sprintf("opsi-curl-probe-%s", sanitizeResourceName(lease.ID))
	defer v.cleanupProbePod(context.Background(), ns, probePodName)

	script := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" --max-time 10 --no-location %q`, targetURL)
	out, err := v.run(ctx, nil, "run", probePodName, "-n", ns,
		"--image=curlimages/curl:8.12.1",
		"--labels=opsi.dev/probe=dep-verification,opsi.dev/lease-id="+sanitizeResourceName(lease.ID),
		"--restart=Never", "--rm", "-i", "--", "sh", "-c", script)
	if err != nil {
		return verificationv1.LayerStatusFailed, 0, verificationv1.FailureConsumerAssertionFailed, "HTTP assertion probe execution failed"
	}

	trimmed := strings.TrimSpace(out)
	statusCode, parseErr := strconv.Atoi(trimmed)
	if parseErr != nil {
		return verificationv1.LayerStatusFailed, 0, verificationv1.FailureConsumerAssertionFailed, fmt.Sprintf("assertion returned invalid status code: %s", trimmed)
	}
	if statusCode == lease.AssertionExpectedCode {
		return verificationv1.LayerStatusVerified, statusCode, "", fmt.Sprintf("HTTP assertion returned expected %d", statusCode)
	}
	return verificationv1.LayerStatusFailed, statusCode, verificationv1.FailureConsumerAssertionFailed, fmt.Sprintf("HTTP assertion returned %d, expected %d", statusCode, lease.AssertionExpectedCode)
}

func (v Verifier) cleanupProbePod(ctx context.Context, namespace, podName string) {
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = v.run(cleanupCtx, nil, "delete", "pod", podName, "-n", namespace, "--ignore-not-found=true", "--grace-period=0", "--force")
}

func (v Verifier) CleanupStaleProbes(ctx context.Context) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := v.run(cleanupCtx, nil, "delete", "pod", "-A", "-l", "opsi.dev/probe=dep-verification", "--ignore-not-found=true", "--grace-period=0", "--force")
	return err
}

func sanitizeResourceName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	res := strings.Trim(b.String(), "-")
	if len(res) > 40 {
		res = res[:40]
	}
	if res == "" {
		res = "probe"
	}
	return res
}

func (v Verifier) run(ctx context.Context, input []byte, args ...string) (string, error) {
	runner := v.Runner
	if runner == nil {
		runner = deploy.ExecCommandRunner{}
	}
	kubectlPath := v.KubectlPath
	if kubectlPath == "" {
		kubectlPath = "kubectl"
	}
	out, err := runner.Run(ctx, input, kubectlPath, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
