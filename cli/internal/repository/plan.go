package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const PlanSchemaVersion = "opsi.cd.plan/v1"

type EventType string

const (
	EventInitial     EventType = "initial"
	EventPush        EventType = "push"
	EventPullRequest EventType = "pull_request"
	EventMerge       EventType = "merge"
)

type PlannerLimits struct {
	MaxDiffBytes       int
	MaxChangedPaths    int
	MaxServices        int
	MaxDependencyDepth int
}

func DefaultPlannerLimits() PlannerLimits {
	return PlannerLimits{MaxDiffBytes: 4 << 20, MaxChangedPaths: 5000, MaxServices: 128, MaxDependencyDepth: 64}
}

type PlanRequest struct {
	Event      EventType
	Base       string
	Head       string
	Repository string
	Config     ConfigV2
}

type PlanReason struct {
	Code        string `json:"code"`
	ServiceKey  string `json:"service_key,omitempty"`
	Dependency  string `json:"dependency,omitempty"`
	Path        string `json:"path,omitempty"`
	Explanation string `json:"explanation"`
}

type ServicePlan struct {
	Key     string       `json:"key"`
	Reasons []PlanReason `json:"reasons"`
}

type BuildTarget struct {
	Key            string   `json:"key"`
	Context        string   `json:"context"`
	Dockerfile     string   `json:"dockerfile"`
	Platform       string   `json:"platform"`
	ProductionRefs []string `json:"production_refs,omitempty"`
}

type ChangedServicePlan struct {
	SchemaVersion       string        `json:"schema_version"`
	Base                string        `json:"base"`
	Head                string        `json:"head"`
	Event               EventType     `json:"event"`
	ConfigHash          string        `json:"config_hash"`
	PlanHash            string        `json:"plan_hash"`
	FullBuild           bool          `json:"full_build"`
	AffectedServiceKeys []string      `json:"affected_service_keys"`
	ReasonCodes         []string      `json:"reason_codes"`
	Services            []ServicePlan `json:"services"`
	Matrix              []BuildTarget `json:"matrix"`
	Explanation         string        `json:"explanation"`
}

type CDService struct {
	Runner CommandRunner
	Limits PlannerLimits
}

func (s CDService) Plan(ctx context.Context, request PlanRequest) (ChangedServicePlan, error) {
	limits := s.Limits
	if limits.MaxDiffBytes == 0 {
		limits = DefaultPlannerLimits()
	}
	if err := validatePlannerLimits(limits); err != nil {
		return ChangedServicePlan{}, err
	}
	if err := ValidateConfig(request.Repository, &request.Config); err != nil {
		return ChangedServicePlan{}, err
	}
	rendered, err := RenderConfigV2(request.Config)
	if err != nil {
		return ChangedServicePlan{}, err
	}
	basePlan := ChangedServicePlan{SchemaVersion: PlanSchemaVersion, Base: request.Base, Head: request.Head, Event: request.Event, ConfigHash: ConfigHash(rendered)}
	if len(request.Config.Services) > limits.MaxServices {
		plan := s.fullPlan(request, "service_limit_exceeded", "The service limit was exceeded, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if !validEvent(request.Event) {
		return ChangedServicePlan{}, fmt.Errorf("unsupported event %q", request.Event)
	}
	if request.Event == EventInitial {
		plan := s.fullPlan(request, "initial_build", "No trusted base exists for an initial build, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if request.Base == "" {
		plan := s.fullPlan(request, "base_missing", "The base revision is missing, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if !validRevision(request.Base) || !validRevision(request.Head) {
		plan := s.fullPlan(request, "revision_untrusted", "A revision is not a full hexadecimal commit ID, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	runner := s.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	shallow, err := runner.Run(ctx, "git", "-C", request.Repository, "rev-parse", "--is-shallow-repository")
	if err != nil {
		plan := s.fullPlan(request, "repository_state_unavailable", "Git repository state could not be verified, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if strings.TrimSpace(string(shallow)) != "false" {
		plan := s.fullPlan(request, "shallow_repository", "The repository is shallow, so the diff base is not trusted and every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if _, err := runner.Run(ctx, "git", "-C", request.Repository, "cat-file", "-e", request.Base+"^{commit}"); err != nil {
		plan := s.fullPlan(request, "base_unavailable", "The base commit is unavailable, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if _, err := runner.Run(ctx, "git", "-C", request.Repository, "cat-file", "-e", request.Head+"^{commit}"); err != nil {
		plan := s.fullPlan(request, "head_unavailable", "The head commit is unavailable, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	diff, err := runner.Run(ctx, "git", "-C", request.Repository, "diff", "--name-status", "-z", request.Base, request.Head)
	if err != nil {
		plan := s.fullPlan(request, "diff_failed", "Git could not produce a trusted diff, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	if len(diff) > limits.MaxDiffBytes {
		plan := s.fullPlan(request, "diff_limit_exceeded", "The diff exceeded its byte limit, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	paths, err := parseNameStatus(diff, limits.MaxChangedPaths)
	if err != nil {
		plan := s.fullPlan(request, "diff_invalid", "Git returned an invalid or ambiguous diff, so every service is selected.")
		plan.ConfigHash = basePlan.ConfigHash
		return finalizePlan(plan, request.Config), nil
	}
	plan := basePlan
	if len(paths) == 0 {
		plan.ReasonCodes = []string{"no_changes"}
		plan.Explanation = "The trusted diff is empty; no services are selected."
		return finalizePlan(plan, request.Config), nil
	}
	plan = resolveChangedPaths(plan, request.Config, paths, limits)
	return finalizePlan(plan, request.Config), nil
}

func (s CDService) fullPlan(request PlanRequest, code, explanation string) ChangedServicePlan {
	plan := ChangedServicePlan{SchemaVersion: PlanSchemaVersion, Base: request.Base, Head: request.Head, Event: request.Event, FullBuild: true, ReasonCodes: []string{code}, Explanation: explanation}
	for _, service := range canonicalConfig(request.Config).Services {
		plan.AffectedServiceKeys = append(plan.AffectedServiceKeys, service.Key)
		plan.Services = append(plan.Services, ServicePlan{Key: service.Key, Reasons: []PlanReason{{Code: code, ServiceKey: service.Key, Explanation: explanation}}})
	}
	return plan
}

func resolveChangedPaths(plan ChangedServicePlan, cfg ConfigV2, changed []string, limits PlannerLimits) ChangedServicePlan {
	cfg = canonicalConfig(cfg)
	reasons := map[string][]PlanReason{}
	direct := map[string]bool{}
	for _, service := range cfg.Services {
		for _, changedPath := range changed {
			code := ""
			switch {
			case pathMatches(service.Build.Dockerfile, changedPath), pathMatches(service.Build.Context, changedPath), matchesAny(service.WatchPaths, changedPath):
				code = "service_path_changed"
			case matchesAny(service.SharedPaths, changedPath):
				code = "shared_path_changed"
			}
			if code != "" {
				direct[service.Key] = true
				reasons[service.Key] = addReason(reasons[service.Key], PlanReason{Code: code, ServiceKey: service.Key, Path: changedPath, Explanation: reasonExplanation(code, service.Key, "", changedPath)})
			}
		}
	}
	reverse := map[string][]string{}
	for _, service := range cfg.Services {
		for _, dep := range service.Dependencies {
			reverse[dep] = append(reverse[dep], service.Key)
		}
	}
	queue := make([]string, 0, len(direct))
	depth := map[string]int{}
	for key := range direct {
		queue = append(queue, key)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		dependency := queue[0]
		queue = queue[1:]
		for _, dependent := range uniqueSorted(reverse[dependency]) {
			if direct[dependent] {
				continue
			}
			if depth[dependent] == 0 || depth[dependent] > depth[dependency]+1 {
				depth[dependent] = depth[dependency] + 1
			}
			if depth[dependent] > limits.MaxDependencyDepth {
				plan.FullBuild = true
				plan.ReasonCodes = []string{"dependency_depth_exceeded"}
				plan.Explanation = "Dependency expansion exceeded its limit, so every service is selected."
				plan.AffectedServiceKeys = nil
				plan.Services = nil
				for _, service := range cfg.Services {
					plan.AffectedServiceKeys = append(plan.AffectedServiceKeys, service.Key)
					plan.Services = append(plan.Services, ServicePlan{Key: service.Key, Reasons: []PlanReason{{Code: "dependency_depth_exceeded", ServiceKey: service.Key, Explanation: plan.Explanation}}})
				}
				return plan
			}
			wasNew := len(reasons[dependent]) == 0
			reasons[dependent] = addReason(reasons[dependent], PlanReason{Code: "dependency_changed", ServiceKey: dependent, Dependency: dependency, Explanation: reasonExplanation("dependency_changed", dependent, dependency, "")})
			if wasNew {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}
	for _, service := range cfg.Services {
		if len(reasons[service.Key]) == 0 {
			continue
		}
		plan.AffectedServiceKeys = append(plan.AffectedServiceKeys, service.Key)
		plan.Services = append(plan.Services, ServicePlan{Key: service.Key, Reasons: reasons[service.Key]})
	}
	if len(plan.AffectedServiceKeys) == 0 {
		plan.FullBuild = true
		plan.ReasonCodes = []string{"unmatched_changes"}
		plan.Explanation = "The trusted diff contains changes outside every configured path, so every service is selected."
		for _, service := range cfg.Services {
			plan.AffectedServiceKeys = append(plan.AffectedServiceKeys, service.Key)
			plan.Services = append(plan.Services, ServicePlan{Key: service.Key, Reasons: []PlanReason{{Code: "unmatched_changes", ServiceKey: service.Key, Explanation: plan.Explanation}}})
		}
	} else {
		plan.Explanation = fmt.Sprintf("%d service(s) are affected by the trusted diff and dependency closure.", len(plan.AffectedServiceKeys))
	}
	for _, service := range plan.Services {
		for _, reason := range service.Reasons {
			plan.ReasonCodes = append(plan.ReasonCodes, reason.Code)
		}
	}
	plan.ReasonCodes = uniqueSorted(plan.ReasonCodes)
	return plan
}

func parseNameStatus(data []byte, maxPaths int) ([]string, error) {
	if len(data) == 0 {
		return []string{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("diff is not NUL terminated")
	}
	tokens := strings.Split(string(data[:len(data)-1]), "\x00")
	paths := []string{}
	for i := 0; i < len(tokens); {
		status := tokens[i]
		i++
		if status == "" {
			return nil, errors.New("empty status")
		}
		count := 1
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && validScore(status[1:]) {
			count = 2
		} else if len(status) != 1 || !strings.Contains("AMDT", status) {
			return nil, fmt.Errorf("unsupported status %q", status)
		}
		if i+count > len(tokens) {
			return nil, errors.New("truncated diff record")
		}
		for _, p := range tokens[i : i+count] {
			if err := validateDiffPath(p); err != nil {
				return nil, err
			}
			paths = append(paths, p)
			if len(paths) > maxPaths {
				return nil, errors.New("changed path limit exceeded")
			}
		}
		i += count
	}
	return uniqueSorted(paths), nil
}

func validateDiffPath(value string) error {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("diff contains an unsafe path")
	}
	return validateSlashRelativePath(value, false)
}

func validScore(value string) bool {
	if len(value) < 1 || len(value) > 3 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func pathMatches(configured, changed string) bool {
	return configured == "." || changed == configured || strings.HasPrefix(changed, configured+"/")
}
func matchesAny(paths []string, changed string) bool {
	for _, p := range paths {
		if pathMatches(p, changed) {
			return true
		}
	}
	return false
}

func addReason(reasons []PlanReason, reason PlanReason) []PlanReason {
	for i := range reasons {
		if reasons[i].Code == reason.Code && reasons[i].Dependency == reason.Dependency {
			if reason.Path < reasons[i].Path {
				reasons[i] = reason
			}
			return reasons
		}
	}
	reasons = append(reasons, reason)
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].Code != reasons[j].Code {
			return reasons[i].Code < reasons[j].Code
		}
		if reasons[i].Dependency != reasons[j].Dependency {
			return reasons[i].Dependency < reasons[j].Dependency
		}
		return reasons[i].Path < reasons[j].Path
	})
	return reasons
}

func reasonExplanation(code, service, dependency, changedPath string) string {
	switch code {
	case "service_path_changed":
		return fmt.Sprintf("Service %s matches changed repository path %s.", service, changedPath)
	case "shared_path_changed":
		return fmt.Sprintf("Service %s declares changed shared path %s.", service, changedPath)
	case "dependency_changed":
		return fmt.Sprintf("Service %s depends on affected service %s.", service, dependency)
	default:
		return "The service is affected."
	}
}

func finalizePlan(plan ChangedServicePlan, cfg ConfigV2) ChangedServicePlan {
	cfg = canonicalConfig(cfg)
	if plan.AffectedServiceKeys == nil {
		plan.AffectedServiceKeys = []string{}
	}
	if plan.ReasonCodes == nil {
		plan.ReasonCodes = []string{}
	}
	if plan.Services == nil {
		plan.Services = []ServicePlan{}
	}
	if plan.Matrix == nil {
		plan.Matrix = []BuildTarget{}
	}
	sort.Strings(plan.AffectedServiceKeys)
	plan.ReasonCodes = uniqueSorted(plan.ReasonCodes)
	sort.Slice(plan.Services, func(i, j int) bool { return plan.Services[i].Key < plan.Services[j].Key })
	selected := map[string]bool{}
	for _, key := range plan.AffectedServiceKeys {
		selected[key] = true
	}
	for _, service := range cfg.Services {
		if selected[service.Key] {
			var productionRefs []string
			if service.Deploy.Production.Enabled {
				for _, branch := range service.Deploy.Production.Branches {
					productionRefs = append(productionRefs, "refs/heads/"+branch)
				}
			}
			plan.Matrix = append(plan.Matrix, BuildTarget{Key: service.Key, Context: service.Build.Context, Dockerfile: service.Build.Dockerfile, Platform: service.Build.Platform, ProductionRefs: productionRefs})
		}
	}
	plan.PlanHash = ""
	encoded, _ := json.Marshal(plan)
	sum := sha256.Sum256(encoded)
	plan.PlanHash = hex.EncodeToString(sum[:])
	return plan
}

func validEvent(event EventType) bool {
	return event == EventInitial || event == EventPush || event == EventPullRequest || event == EventMerge
}
func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
func validatePlannerLimits(l PlannerLimits) error {
	if l.MaxDiffBytes < 1 || l.MaxChangedPaths < 1 || l.MaxServices < 1 || l.MaxDependencyDepth < 1 {
		return errors.New("planner limits must be positive")
	}
	return nil
}
