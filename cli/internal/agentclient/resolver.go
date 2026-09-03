package agentclient

import (
	"context"
	"errors"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type CoverageStatus string

const (
	CoverageConnected   CoverageStatus = "connected"
	CoveragePartial     CoverageStatus = "partial"
	CoverageUnavailable CoverageStatus = "unavailable"
)

const (
	DiagCloudAuthRequired  = "CLOUD_AUTH_REQUIRED"
	DiagCloudUnavailable   = "CLOUD_UNAVAILABLE"
	DiagNoObservableAgents = "NO_OBSERVABLE_AGENTS"
	DiagAgentUnreachable   = "AGENT_UNREACHABLE"
	DiagTLSPinMismatch     = "TLS_PIN_MISMATCH"
	DiagAgentTimeout       = "AGENT_TIMEOUT"
	DiagClientMTLSMissing  = "CLIENT_MTLS_MISSING"
	DiagClientMTLSInvalid  = "CLIENT_MTLS_INVALID"
	DiagAgentAuthRequired  = "AGENT_AUTH_REQUIRED"
	DiagAgentOperationFail = "AGENT_OPERATION_FAILED"
)

// AgentDiagnosticError represents a sanitized, node-attributed diagnostic issue.
type AgentDiagnosticError struct {
	NodeID          string `json:"node_id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	Code            string `json:"code"`
	MessageRedacted string `json:"message_redacted"`
	ActionableCause string `json:"actionable_cause,omitempty"`
}

// TelemetryCoverage describes observation coverage across project nodes.
type TelemetryCoverage struct {
	Status           CoverageStatus         `json:"status"`
	ExpectedAgents   int                    `json:"expected_agents"`
	SuccessfulAgents int                    `json:"successful_agents"`
	FailedAgents     int                    `json:"failed_agents"`
	Errors           []AgentDiagnosticError `json:"errors,omitempty"`
	ObservedAt       time.Time              `json:"observed_at"`
}

// ProjectAgentConnectionResponse reports the live connectivity status of project agents.
type ProjectAgentConnectionResponse struct {
	ProjectID        string                 `json:"project_id"`
	Status           CoverageStatus         `json:"status"`
	ExpectedAgents   int                    `json:"expected_agents"`
	SuccessfulAgents int                    `json:"successful_agents"`
	FailedAgents     int                    `json:"failed_agents"`
	Errors           []AgentDiagnosticError `json:"errors,omitempty"`
	ObservedAt       time.Time              `json:"observed_at"`
}

// TelemetryUnavailableError is a typed error indicating all project agents failed or no observable agents were reached.
type TelemetryUnavailableError struct {
	Coverage TelemetryCoverage
	Message  string
}

func (e *TelemetryUnavailableError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "Agent telemetry unavailable"
}

// NodeLister abstracts listing nodes from Cloud registry.
type NodeLister interface {
	ListNodes(ctx context.Context, projectID string) ([]cloudclient.Node, error)
}

// TelemetryClient abstracts agent interactions.
type TelemetryClient interface {
	Status(ctx context.Context) (*agentv1.StatusResponse, error)
	QueryTelemetry(ctx context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error)
}

// Target represents a discovered, valid observable Agent target.
type Target struct {
	NodeID        string
	AgentID       string
	Endpoint      string
	Port          int
	TLSServerName string
	CertSHA256    string
	Status        string
	Config        config.Config
}

type AgentTargetResolverOptions struct {
	CloudClient   NodeLister
	BaseConfig    config.Config
	PAT           string
	ClientFactory func(cfg config.Config) TelemetryClient
	Concurrency   int
	QueryTimeout  time.Duration
}

// AgentTargetResolver is the single authority for project-scoped Agent observability and telemetry operations.
type AgentTargetResolver struct {
	cloudClient   NodeLister
	baseConfig    config.Config
	pat           string
	clientFactory func(cfg config.Config) TelemetryClient
	concurrency   int
	queryTimeout  time.Duration
}

func acquireAgentSlot(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func preDialErrorByNode(errorsList []AgentDiagnosticError) map[string]AgentDiagnosticError {
	result := make(map[string]AgentDiagnosticError, len(errorsList))
	for _, diagnostic := range errorsList {
		if diagnostic.NodeID != "" {
			result[diagnostic.NodeID] = diagnostic
		}
	}
	return result
}

func NewAgentTargetResolver(opts AgentTargetResolverOptions) *AgentTargetResolver {
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	queryTimeout := opts.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 2 * time.Second
	}
	factory := opts.ClientFactory
	if factory == nil {
		factory = func(cfg config.Config) TelemetryClient {
			return New(cfg)
		}
	}
	return &AgentTargetResolver{
		cloudClient:   opts.CloudClient,
		baseConfig:    opts.BaseConfig,
		pat:           opts.PAT,
		clientFactory: factory,
		concurrency:   concurrency,
		queryTimeout:  queryTimeout,
	}
}

// isObservableNodeStatus checks if a node is in an observable state.
func isObservableNodeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "healthy", "ready", "active", "online", "connected", "agent_connecting", "draining":
		return true
	default:
		return false
	}
}

// isValidObservableNode validates endpoint, port, certificate pin, and observable status.
func isValidObservableNode(node cloudclient.Node) bool {
	endpoint := strings.TrimSpace(node.AgentEndpoint)
	if endpoint == "" {
		return false
	}
	if node.AgentPort < 1 || node.AgentPort > 65535 {
		return false
	}
	pin := normalizeFingerprint(node.AgentCertSHA256)
	if len(pin) != 64 {
		return false
	}
	if !isObservableNodeStatus(node.Status) {
		return false
	}
	return true
}

func isNilNodeLister(nl NodeLister) bool {
	if nl == nil {
		return true
	}
	if client, ok := nl.(*cloudclient.Client); ok && client == nil {
		return true
	}
	return false
}

// ResolveTargets queries Cloud for project nodes and filters valid observable targets with TLS pinning.
// NEVER falls back to 127.0.0.1:9443 or local agent_addr.
func (r *AgentTargetResolver) ResolveTargets(ctx context.Context, projectID string) ([]Target, []AgentDiagnosticError, error) {
	if isNilNodeLister(r.cloudClient) {
		return nil, nil, &TelemetryUnavailableError{
			Coverage: TelemetryCoverage{
				Status:     CoverageUnavailable,
				ObservedAt: time.Now().UTC(),
				Errors: []AgentDiagnosticError{{
					Code:            DiagCloudUnavailable,
					MessageRedacted: "cloud client not configured",
					ActionableCause: "Cloud client is not configured to resolve project agents.",
				}},
			},
			Message: "cloud client not configured",
		}
	}

	nodes, err := r.cloudClient.ListNodes(ctx, projectID)
	if err != nil {
		code := DiagCloudUnavailable
		actionable := "Failed to reach Cloud registry to discover project agents."
		var apiErr *cloudclient.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 401 || apiErr.Code == "UNAUTHENTICATED" || apiErr.Code == "CLOUD_AUTH_REQUIRED") {
			code = DiagCloudAuthRequired
			actionable = "Cloud PAT is missing, expired, or rejected. Log in to refresh local authentication."
		} else if strings.Contains(strings.ToLower(err.Error()), "401") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			code = DiagCloudAuthRequired
			actionable = "Cloud PAT is missing, expired, or rejected. Log in to refresh local authentication."
		}

		diagErr := AgentDiagnosticError{
			Code:            code,
			MessageRedacted: redactSecrets(err.Error()),
			ActionableCause: actionable,
		}
		return nil, []AgentDiagnosticError{diagErr}, &TelemetryUnavailableError{
			Coverage: TelemetryCoverage{
				Status:     CoverageUnavailable,
				ObservedAt: time.Now().UTC(),
				Errors:     []AgentDiagnosticError{diagErr},
			},
			Message: diagErr.MessageRedacted,
		}
	}

	var targets []Target
	var preDialErrors []AgentDiagnosticError

	// Check local client mTLS files if configured
	var mTLSErr *AgentDiagnosticError
	if r.baseConfig.TLS.ClientCertPath != "" || r.baseConfig.TLS.ClientKeyPath != "" {
		if r.baseConfig.TLS.ClientCertPath == "" || r.baseConfig.TLS.ClientKeyPath == "" {
			mTLSErr = &AgentDiagnosticError{
				Code:            DiagClientMTLSMissing,
				MessageRedacted: "both client_cert_path and client_key_path are required for mTLS",
				ActionableCause: "Local client mTLS certificate or key file is missing. Verify tls.client_cert_path and tls.client_key_path.",
			}
		} else if _, err := os.Stat(r.baseConfig.TLS.ClientCertPath); err != nil {
			mTLSErr = &AgentDiagnosticError{
				Code:            DiagClientMTLSMissing,
				MessageRedacted: "client cert file not found",
				ActionableCause: "Local client mTLS certificate file is missing. Verify tls.client_cert_path.",
			}
		} else if _, err := os.Stat(r.baseConfig.TLS.ClientKeyPath); err != nil {
			mTLSErr = &AgentDiagnosticError{
				Code:            DiagClientMTLSMissing,
				MessageRedacted: "client key file not found",
				ActionableCause: "Local client mTLS private key file is missing. Verify tls.client_key_path.",
			}
		}
	}

	for _, node := range nodes {
		if !isValidObservableNode(node) {
			continue
		}

		serverName := node.AgentTLSServerName
		if serverName == "" {
			serverName = node.AgentEndpoint
		}

		targetCfg := config.Config{
			AgentAddr: net.JoinHostPort(node.AgentEndpoint, strconv.Itoa(node.AgentPort)),
			CloudURL:  r.baseConfig.CloudURL,
			TLS: config.TLSConfig{
				ServerName:             serverName,
				PinnedServerCertSHA256: node.AgentCertSHA256,
				ClientCertPath:         r.baseConfig.TLS.ClientCertPath,
				ClientKeyPath:          r.baseConfig.TLS.ClientKeyPath,
				CACertPath:             r.baseConfig.TLS.CACertPath,
			},
		}

		targets = append(targets, Target{
			NodeID:        node.ID,
			AgentID:       node.AgentID,
			Endpoint:      node.AgentEndpoint,
			Port:          node.AgentPort,
			TLSServerName: serverName,
			CertSHA256:    node.AgentCertSHA256,
			Status:        node.Status,
			Config:        targetCfg,
		})

		if mTLSErr != nil {
			errCopy := *mTLSErr
			errCopy.NodeID = node.ID
			errCopy.AgentID = node.AgentID
			errCopy.Endpoint = node.AgentEndpoint
			preDialErrors = append(preDialErrors, errCopy)
		}
	}

	return targets, preDialErrors, nil
}

// CheckConnection probes all observable project agents and returns comprehensive connectivity diagnostics.
func (r *AgentTargetResolver) CheckConnection(ctx context.Context, projectID string) (ProjectAgentConnectionResponse, error) {
	observedAt := time.Now().UTC()
	targets, preDialErrors, err := r.ResolveTargets(ctx, projectID)
	if err != nil {
		var unavail *TelemetryUnavailableError
		if errors.As(err, &unavail) {
			return ProjectAgentConnectionResponse{
				ProjectID:        projectID,
				Status:           CoverageUnavailable,
				ExpectedAgents:   0,
				SuccessfulAgents: 0,
				FailedAgents:     0,
				Errors:           unavail.Coverage.Errors,
				ObservedAt:       observedAt,
			}, nil
		}
		return ProjectAgentConnectionResponse{
			ProjectID:        projectID,
			Status:           CoverageUnavailable,
			ExpectedAgents:   0,
			SuccessfulAgents: 0,
			FailedAgents:     0,
			Errors: []AgentDiagnosticError{{
				Code:            DiagCloudUnavailable,
				MessageRedacted: redactSecrets(err.Error()),
				ActionableCause: "Failed to resolve project agents from Cloud registry.",
			}},
			ObservedAt: observedAt,
		}, nil
	}

	if len(targets) == 0 {
		return ProjectAgentConnectionResponse{
			ProjectID:        projectID,
			Status:           CoverageUnavailable,
			ExpectedAgents:   0,
			SuccessfulAgents: 0,
			FailedAgents:     0,
			Errors: []AgentDiagnosticError{{
				Code:            DiagNoObservableAgents,
				MessageRedacted: "no observable agents registered in project",
				ActionableCause: "Ensure the project has healthy servers with active agents registered in Cloud.",
			}},
			ObservedAt: observedAt,
		}, nil
	}

	type probeResult struct {
		target  Target
		status  *agentv1.StatusResponse
		diagErr *AgentDiagnosticError
	}

	results := make([]probeResult, len(targets))
	sem := make(chan struct{}, r.concurrency)
	preDialByNode := preDialErrorByNode(preDialErrors)
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		go func(idx int, tgt Target) {
			defer wg.Done()
			if diagnostic, invalid := preDialByNode[tgt.NodeID]; invalid {
				results[idx] = probeResult{target: tgt, diagErr: &diagnostic}
				return
			}
			if !acquireAgentSlot(ctx, sem) {
				diagnostic := r.classifyError(tgt, context.DeadlineExceeded)
				results[idx] = probeResult{target: tgt, diagErr: &diagnostic}
				return
			}
			defer func() { <-sem }()

			callCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
			defer cancel()
			if r.pat != "" {
				callCtx = WithPAT(callCtx, r.pat)
			}

			client := r.clientFactory(tgt.Config)
			statusResp, probeErr := client.Status(callCtx)
			if probeErr != nil {
				diag := r.classifyError(tgt, probeErr)
				results[idx] = probeResult{target: tgt, diagErr: &diag}
			} else {
				results[idx] = probeResult{target: tgt, status: statusResp}
			}
		}(i, target)
	}
	wg.Wait()

	var successful, failed int
	var errorsList []AgentDiagnosticError

	for _, res := range results {
		if res.diagErr != nil {
			failed++
			errorsList = append(errorsList, *res.diagErr)
		} else {
			successful++
		}
	}

	status := CoverageConnected
	if successful == 0 {
		status = CoverageUnavailable
	} else if successful < len(targets) {
		status = CoveragePartial
	}

	return ProjectAgentConnectionResponse{
		ProjectID:        projectID,
		Status:           status,
		ExpectedAgents:   len(targets),
		SuccessfulAgents: successful,
		FailedAgents:     failed,
		Errors:           errorsList,
		ObservedAt:       observedAt,
	}, nil
}

// QueryTelemetry queries telemetry across all observable project agents, aggregating partial results or returning AGENT_TELEMETRY_UNAVAILABLE.
func (r *AgentTargetResolver) QueryTelemetry(ctx context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, *TelemetryCoverage, error) {
	observedAt := time.Now().UTC()
	targets, preDialErrors, err := r.ResolveTargets(ctx, req.ProjectID)
	if err != nil {
		var unavail *TelemetryUnavailableError
		if errors.As(err, &unavail) {
			return nil, &unavail.Coverage, unavail
		}
		cov := TelemetryCoverage{
			Status:     CoverageUnavailable,
			ObservedAt: observedAt,
			Errors: []AgentDiagnosticError{{
				Code:            DiagCloudUnavailable,
				MessageRedacted: redactSecrets(err.Error()),
				ActionableCause: "Failed to resolve project agents from Cloud registry.",
			}},
		}
		return nil, &cov, &TelemetryUnavailableError{Coverage: cov, Message: err.Error()}
	}

	if len(targets) == 0 {
		cov := TelemetryCoverage{
			Status:     CoverageUnavailable,
			ObservedAt: observedAt,
			Errors: []AgentDiagnosticError{{
				Code:            DiagNoObservableAgents,
				MessageRedacted: "no observable agents registered in project",
				ActionableCause: "Ensure the project has healthy servers with active agents registered in Cloud.",
			}},
		}
		return nil, &cov, &TelemetryUnavailableError{Coverage: cov, Message: "no observable agents registered in project"}
	}

	type queryResult struct {
		target   Target
		response *agentv1.TelemetryQueryResponse
		diagErr  *AgentDiagnosticError
		rawErr   error
	}

	results := make([]queryResult, len(targets))
	sem := make(chan struct{}, r.concurrency)
	preDialByNode := preDialErrorByNode(preDialErrors)
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		go func(idx int, tgt Target) {
			defer wg.Done()
			if diagnostic, invalid := preDialByNode[tgt.NodeID]; invalid {
				results[idx] = queryResult{target: tgt, diagErr: &diagnostic}
				return
			}
			if !acquireAgentSlot(ctx, sem) {
				diagnostic := r.classifyError(tgt, context.DeadlineExceeded)
				results[idx] = queryResult{target: tgt, diagErr: &diagnostic, rawErr: context.DeadlineExceeded}
				return
			}
			defer func() { <-sem }()

			callCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
			defer cancel()
			if r.pat != "" {
				callCtx = WithPAT(callCtx, r.pat)
			}

			client := r.clientFactory(tgt.Config)
			resp, queryErr := client.QueryTelemetry(callCtx, req)
			if queryErr != nil {
				diag := r.classifyError(tgt, queryErr)
				results[idx] = queryResult{target: tgt, diagErr: &diag, rawErr: queryErr}
			} else {
				results[idx] = queryResult{target: tgt, response: resp}
			}
		}(i, target)
	}
	wg.Wait()

	var successful, failed int
	var errorsList []AgentDiagnosticError

	var successfulResponses []*agentv1.TelemetryQueryResponse
	for _, res := range results {
		if res.diagErr != nil {
			failed++
			errorsList = append(errorsList, *res.diagErr)
		} else if res.response != nil {
			successful++
			successfulResponses = append(successfulResponses, res.response)
		}
	}
	if successful == 0 {
		for _, res := range results {
			if res.rawErr != nil && grpcstatus.Code(res.rawErr) == codes.InvalidArgument {
				return nil, nil, res.rawErr
			}
		}
		cov := TelemetryCoverage{
			Status:           CoverageUnavailable,
			ExpectedAgents:   len(targets),
			SuccessfulAgents: 0,
			FailedAgents:     failed,
			Errors:           errorsList,
			ObservedAt:       observedAt,
		}
		return nil, &cov, &TelemetryUnavailableError{Coverage: cov, Message: "all project agents failed to respond to telemetry query"}
	}

	status := CoverageConnected
	if successful < len(targets) {
		status = CoveragePartial
	}

	coverage := &TelemetryCoverage{
		Status:           status,
		ExpectedAgents:   len(targets),
		SuccessfulAgents: successful,
		FailedAgents:     failed,
		Errors:           errorsList,
		ObservedAt:       observedAt,
	}

	merged := mergeTelemetryResponses(req.ProjectID, successfulResponses)
	return merged, coverage, nil
}

func (r *AgentTargetResolver) classifyError(target Target, err error) AgentDiagnosticError {
	msg := err.Error()
	code := DiagAgentOperationFail
	actionable := "Agent telemetry operation failed. Check agent logs for details."

	isTimeout := errors.Is(err, context.DeadlineExceeded) ||
		grpcstatus.Code(err) == codes.DeadlineExceeded ||
		strings.Contains(strings.ToLower(msg), "context deadline exceeded") ||
		strings.Contains(strings.ToLower(msg), "timeout")

	isPinMismatch := strings.Contains(strings.ToLower(msg), "server certificate pin mismatch") ||
		strings.Contains(strings.ToLower(msg), "certificate pin mismatch") ||
		strings.Contains(strings.ToLower(msg), "server certificate name mismatch")

	isMTLS := strings.Contains(strings.ToLower(msg), "load client key pair") ||
		strings.Contains(strings.ToLower(msg), "client key pair") ||
		strings.Contains(strings.ToLower(msg), "read ca cert") ||
		strings.Contains(strings.ToLower(msg), "parse ca cert")

	isUnreachable := strings.Contains(strings.ToLower(msg), "connection refused") ||
		strings.Contains(strings.ToLower(msg), "no route to host") ||
		strings.Contains(strings.ToLower(msg), "network is unreachable") ||
		grpcstatus.Code(err) == codes.Unavailable

	isAuth := grpcstatus.Code(err) == codes.Unauthenticated ||
		grpcstatus.Code(err) == codes.PermissionDenied ||
		strings.Contains(strings.ToLower(msg), "unauthenticated") ||
		strings.Contains(strings.ToLower(msg), "permission denied")

	switch {
	case isPinMismatch:
		code = DiagTLSPinMismatch
		actionable = "Agent TLS certificate does not match the pinned certificate SHA-256 registered in Cloud."
	case isMTLS:
		code = DiagClientMTLSMissing
		actionable = "Local client mTLS certificate or private key is missing or invalid. Verify tls.client_cert_path and tls.client_key_path."
	case isTimeout:
		code = DiagAgentTimeout
		actionable = "Connection or query timed out. Verify workstation can reach agent TLS port and VPS firewall allows traffic."
	case isUnreachable:
		code = DiagAgentUnreachable
		actionable = "Agent endpoint is unreachable (connection refused, host down, or network route blocked)."
	case isAuth:
		code = DiagAgentAuthRequired
		actionable = "Agent rejected the connection token. Verify a valid Cloud PAT is stored in the local keychain."
	}

	return AgentDiagnosticError{
		NodeID:          target.NodeID,
		AgentID:         target.AgentID,
		Endpoint:        target.Endpoint,
		Code:            code,
		MessageRedacted: redactSecrets(msg),
		ActionableCause: actionable,
	}
}

func mergeTelemetryResponses(projectID string, responses []*agentv1.TelemetryQueryResponse) *agentv1.TelemetryQueryResponse {
	if len(responses) == 0 {
		return &agentv1.TelemetryQueryResponse{
			ProjectID:     projectID,
			Source:        "agent",
			PayloadPolicy: "redacted summaries",
		}
	}

	merged := &agentv1.TelemetryQueryResponse{
		ProjectID:     projectID,
		Source:        "agent",
		PayloadPolicy: responses[0].PayloadPolicy,
	}

	// Merge summaries
	var totalMetrics, totalLogs, totalErrors, totalServices int32
	var minSince, maxEnd int64
	var healthRank = map[string]int{"critical": 4, "failed": 3, "degraded": 2, "healthy": 1, "": 0}
	var worstHealth = "healthy"
	hasSummary := false

	for _, resp := range responses {
		if resp.Summary == nil {
			continue
		}
		hasSummary = true
		s := resp.Summary
		totalMetrics += s.MetricCount
		totalLogs += s.LogCount
		totalErrors += s.ErrorCount
		totalServices += s.ServiceCount
		if minSince == 0 || (s.SinceUnix > 0 && s.SinceUnix < minSince) {
			minSince = s.SinceUnix
		}
		if s.EndUnix > maxEnd {
			maxEnd = s.EndUnix
		}
		if healthRank[strings.ToLower(s.Health)] > healthRank[strings.ToLower(worstHealth)] {
			worstHealth = s.Health
		}
	}

	if hasSummary {
		merged.Summary = &agentv1.TelemetryRuntimeSummary{
			SinceUnix:    minSince,
			EndUnix:      maxEnd,
			MetricCount:  totalMetrics,
			LogCount:     totalLogs,
			ErrorCount:   totalErrors,
			ServiceCount: totalServices,
			Health:       worstHealth,
		}
	}

	// Merge services by service_id
	serviceMap := make(map[string]agentv1.TelemetryServiceStatus)
	for _, resp := range responses {
		for _, svc := range resp.Services {
			existing, found := serviceMap[svc.ServiceID]
			if !found {
				serviceMap[svc.ServiceID] = svc
				continue
			}
			existing.PodCount += svc.PodCount
			existing.ReadyPods += svc.ReadyPods
			existing.CPUCores += svc.CPUCores
			existing.MemoryBytes += svc.MemoryBytes
			existing.RestartCount += svc.RestartCount
			existing.RecentErrorCount += svc.RecentErrorCount
			if svc.LastSeenUnix > existing.LastSeenUnix {
				existing.LastSeenUnix = svc.LastSeenUnix
			}
			if healthRank[strings.ToLower(svc.Health)] > healthRank[strings.ToLower(existing.Health)] {
				existing.Health = svc.Health
			}
			serviceMap[svc.ServiceID] = existing
		}
	}

	var servicesList []agentv1.TelemetryServiceStatus
	for _, svc := range serviceMap {
		servicesList = append(servicesList, svc)
	}
	sort.Slice(servicesList, func(i, j int) bool {
		return servicesList[i].ServiceID < servicesList[j].ServiceID
	})
	merged.Services = servicesList

	// Merge logs
	var logsList []agentv1.TelemetryLogEntry
	for _, resp := range responses {
		logsList = append(logsList, resp.Logs...)
	}
	sort.Slice(logsList, func(i, j int) bool {
		return logsList[i].ObservedUnix > logsList[j].ObservedUnix
	})
	merged.Logs = logsList

	return merged
}

var tokenPattern = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9_\-\.]+)|([a-f0-9]{32,64})|(pat-[A-Za-z0-9_\-]+)`)
var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(password|token|secret|api[_-]?key|private[ _-]?key)\s*([=:])\s*[^\s,;]+`)

func redactSecrets(text string) string {
	redacted := sensitiveAssignmentPattern.ReplaceAllString(text, "$1$2[REDACTED]")
	return tokenPattern.ReplaceAllStringFunc(redacted, func(m string) string {
		lower := strings.ToLower(m)
		if strings.HasPrefix(lower, "bearer ") {
			return "Bearer [REDACTED]"
		}
		return "[REDACTED]"
	})
}
