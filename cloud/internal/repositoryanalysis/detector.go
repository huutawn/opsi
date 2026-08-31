package repositoryanalysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	"gopkg.in/yaml.v3"
)

type File struct {
	Path string
	Size int64
	Mode string
}

type Repository interface {
	ListFiles(context.Context, int64, string, string) ([]File, bool, error)
	ReadFile(context.Context, int64, string, string, string, int64) ([]byte, error)
}

type Request struct {
	InstallationID int64
	RepositoryID   int64
	Repository     string
	SelectedRef    string
	CommitSHA      string
	Scope          Scope
}

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxDuration   time.Duration
}

func DefaultLimits() Limits {
	return Limits{MaxFiles: 128, MaxFileBytes: 512 << 10, MaxTotalBytes: 8 << 20, MaxDuration: 20 * time.Second}
}

type Detector struct {
	Repository Repository
	Limits     Limits
	Now        func() time.Time
}

func (d Detector) Analyze(ctx context.Context, request Request) (Result, error) {
	if d.Repository == nil || request.InstallationID <= 0 || request.RepositoryID <= 0 || request.Repository == "" || len(request.CommitSHA) != 40 {
		return Result{}, errors.New("repository analysis request is invalid")
	}
	limits := d.Limits
	if limits.MaxFiles == 0 {
		limits = DefaultLimits()
	}
	if limits.MaxFiles < 1 || limits.MaxFileBytes < 1 || limits.MaxTotalBytes < 1 || limits.MaxDuration <= 0 {
		return Result{}, errors.New("repository analysis limits are invalid")
	}
	analysisCtx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	scope, scopeHash, err := canonicalScope(request.Scope)
	if err != nil {
		return Result{}, err
	}
	request.Scope = scope
	files, treeTruncated, err := d.Repository.ListFiles(analysisCtx, request.InstallationID, request.Repository, request.CommitSHA)
	if err != nil {
		return Result{}, fmt.Errorf("list exact repository snapshot: %w", err)
	}
	result := Result{
		SchemaVersion: SchemaVersion, RepositoryID: request.RepositoryID, Repository: request.Repository,
		SelectedRef: request.SelectedRef, CommitSHA: strings.ToLower(request.CommitSHA), Authority: "heuristics",
		Applications: []Application{}, Resources: []Resource{}, Dependencies: []Dependency{},
		Bindings: []Binding{}, Secrets: []Secret{}, Issues: []Issue{},
		AnalyzedAt: d.clock(), Scope: scope, ScopeHash: scopeHash,
	}
	truncationReasons := map[string]bool{}
	if treeTruncated {
		truncationReasons["tree"] = true
	}
	fileSet := make(map[string]File, len(files))
	for _, file := range files {
		if !safePath(file.Path, false) || file.Size < 0 {
			result.Issues = append(result.Issues, Issue{Code: "REPOSITORY_PATH_INVALID", Message: "Repository contains a non-canonical file path.", Path: file.Path, Resolution: "Remove the invalid path from the selected commit.", Blocking: true})
			continue
		}
		if file.Mode == "120000" {
			if !scopeIncludes(file.Path, scope) {
				continue
			}
			result.Issues = append(result.Issues, Issue{Code: "REPOSITORY_SYMLINK_UNSUPPORTED", Message: "Repository analysis does not follow symbolic links.", Path: file.Path, Resolution: "Replace the symlink with a regular file or exclude it from deployable intent.", Blocking: true})
			continue
		}
		if scopeIncludes(file.Path, scope) {
			fileSet[file.Path] = file
		}
	}
	candidates := selectAnalysisCandidates(fileSet)
	result.EvidenceCoverage.CandidatesFound = len(candidates)
	if len(candidates) > limits.MaxFiles {
		candidates = candidates[:limits.MaxFiles]
		truncationReasons["file_count"] = true
	}
	selected := make([]File, 0, len(candidates))
	var reservedBytes int64
	for _, file := range candidates {
		if file.Size > limits.MaxFileBytes {
			truncationReasons["blob_size"] = true
			continue
		}
		if reservedBytes+file.Size > limits.MaxTotalBytes {
			truncationReasons["total_bytes"] = true
			continue
		}
		reservedBytes += file.Size
		selected = append(selected, file)
	}
	result.EvidenceCoverage.CandidatesSelected = len(selected)
	contents, readErrors := d.readCandidates(analysisCtx, request, selected, limits.MaxFileBytes)
	for _, file := range selected {
		if readErr, ok := readErrors[file.Path]; ok {
			if errors.Is(readErr, errBlobSizeLimit) {
				truncationReasons["blob_size"] = true
				continue
			}
			if analysisCtx.Err() != nil || errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, context.Canceled) {
				truncationReasons["deadline"] = true
				continue
			}
			return Result{}, fmt.Errorf("read repository candidate %s: %w", file.Path, readErr)
		}
	}
	read := func(filePath string) ([]byte, error) {
		if data, ok := contents[filePath]; ok {
			return data, nil
		}
		if readErr, ok := readErrors[filePath]; ok {
			return nil, readErr
		}
		return nil, errors.New("file was not selected for bounded analysis")
	}
	for _, file := range selected {
		if data, ok := contents[file.Path]; ok {
			result.FilesInspected++
			result.BytesInspected += int64(len(data))
			if int64(len(data)) > limits.MaxFileBytes {
				truncationReasons["blob_size"] = true
			}
		}
	}
	result.EvidenceCoverage.FilesInspected = result.FilesInspected
	result.EvidenceCoverage.BytesInspected = result.BytesInspected
	if analysisCtx.Err() != nil {
		truncationReasons["deadline"] = true
	}

	explicitAuthority := false
	explicitUnavailable := false
	if _, ok := fileSet[".opsi/opsi-cd.yaml"]; ok {
		data, readErr := read(".opsi/opsi-cd.yaml")
		if readErr != nil {
			explicitUnavailable = true
			result.Authority = "explicit_config_unreadable"
			result.Issues = append(result.Issues, Issue{Code: "EXPLICIT_CONFIG_UNREADABLE", Message: "The explicit repository configuration could not be read within the analysis limits.", Path: ".opsi/opsi-cd.yaml", Resolution: "Reduce the configuration file below the blob limit, then analyze the same commit again.", Blocking: true})
		} else if explicit, parseErr := parseExplicit(data, fileSet); parseErr != nil {
			result.Authority = "explicit_config_invalid"
			result.Issues = append(result.Issues, Issue{Code: "EXPLICIT_CONFIG_INVALID", Message: parseErr.Error(), Path: ".opsi/opsi-cd.yaml", Resolution: "Correct the repository configuration at this commit, then analyze again.", Blocking: true})
			return result, nil
		} else {
			result.Authority = "explicit_config"
			explicitAuthority = true
			result.Applications, result.Resources, result.Dependencies, result.Bindings, result.Secrets = explicit.Applications, explicit.Resources, explicit.Dependencies, explicit.Bindings, explicit.Secrets
		}
	}
	if composePath := firstExisting(fileSet, "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"); composePath != "" {
		data, readErr := read(composePath)
		if readErr == nil {
			apps, resources, deps, issues := parseCompose(data, composePath, fileSet)
			if explicitAuthority {
				mergeCompose(&result, apps, resources, deps, issues)
			} else if !explicitUnavailable && (len(apps) > 0 || len(resources) > 0) {
				result.Authority, result.Applications, result.Resources, result.Dependencies, result.Issues = "compose", apps, resources, deps, issues
			}
		}
	}
	analysisFiles := selected
	if len(result.Applications) == 0 {
		result.Applications = dockerfileApplications(analysisFiles, read)
		if len(result.Applications) == 0 {
			result.Applications = frameworkOrManifestApplications(analysisFiles, read)
		}
		if len(result.Applications) == 0 {
			result.Issues = append(result.Issues, Issue{Code: "NO_DEPLOYABLE_APPLICATION", Message: "No explicit service, Compose build, or Dockerfile could be identified.", Resolution: "Add .opsi/opsi-cd.yaml or select deployable application roots manually.", Blocking: true})
		}
	}
	canonicalizeResult(request.Repository, &result)
	enrichApplications(&result, fileSet, read)
	inferDependencies(&result, analysisFiles, read)
	validateDetected(&result)
	if len(truncationReasons) > 0 {
		result.Truncated = true
		result.TruncationReason = firstTruncationReason(truncationReasons)
		result.Issues = append(result.Issues, Issue{Code: "ANALYSIS_TRUNCATED", Message: "Repository analysis was truncated by the " + result.TruncationReason + " limit.", Resolution: "Refine analysis with application roots or exclude paths, then analyze the same exact commit again.", Blocking: true})
	}
	sortResult(&result)
	return result, nil
}

func canonicalScope(scope Scope) (Scope, string, error) {
	canonical := Scope{ApplicationRoots: append([]string(nil), scope.ApplicationRoots...), ExcludePaths: append([]string(nil), scope.ExcludePaths...)}
	for i, root := range canonical.ApplicationRoots {
		canonical.ApplicationRoots[i] = cleanRoot(root)
		if !safePath(canonical.ApplicationRoots[i], true) {
			return Scope{}, "", errors.New("analysis application root is invalid")
		}
	}
	for i, excluded := range canonical.ExcludePaths {
		canonical.ExcludePaths[i] = cleanRoot(excluded)
		if !safePath(canonical.ExcludePaths[i], false) {
			return Scope{}, "", errors.New("analysis exclude path is invalid")
		}
	}
	canonical.ApplicationRoots = uniqueSorted(canonical.ApplicationRoots)
	canonical.ExcludePaths = uniqueSorted(canonical.ExcludePaths)
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func firstTruncationReason(reasons map[string]bool) string {
	for _, reason := range []string{"tree", "file_count", "blob_size", "total_bytes", "deadline"} {
		if reasons[reason] {
			return reason
		}
	}
	return "deadline"
}

type explicitConfig struct {
	Version   int `yaml:"version"`
	Resources []struct {
		LogicalName string            `yaml:"logicalName"`
		Type        string            `yaml:"type"`
		Managed     bool              `yaml:"managed"`
		Required    bool              `yaml:"required"`
		Persistence *Persistence      `yaml:"persistence"`
		Settings    map[string]string `yaml:"settings"`
	} `yaml:"resources"`
	Services []struct {
		Key          string                                         `yaml:"key"`
		Build        struct{ Context, Dockerfile, Platform string } `yaml:"build"`
		WatchPaths   []string                                       `yaml:"watchPaths"`
		SharedPaths  []string                                       `yaml:"sharedPaths"`
		Dependencies []string                                       `yaml:"dependencies"`
		Runtime      *struct {
			Port        int               `yaml:"port"`
			Environment map[string]string `yaml:"environment"`
			Capacity    *Capacity         `yaml:"capacity"`
			Exposure    *Exposure         `yaml:"exposure"`
			Secrets     []struct {
				LogicalName     string `yaml:"logicalName"`
				EnvironmentName string `yaml:"environmentName"`
				Source          string `yaml:"source"`
				Reference       string `yaml:"reference"`
			} `yaml:"secrets"`
			Bindings []struct {
				Kind   string `yaml:"kind"`
				Target string `yaml:"target"`
				Path   string `yaml:"path"`
			} `yaml:"bindings"`
			Dependencies []struct {
				Target     string `yaml:"target"`
				Protocol   string `yaml:"protocol"`
				Strategy   string `yaml:"strategy"`
				Path       string `yaml:"path"`
				Required   bool   `yaml:"required"`
				Injections []struct {
					EnvironmentName string `yaml:"environmentName"`
					SymbolicSource  string `yaml:"symbolicSource"`
					Template        string `yaml:"template"`
				} `yaml:"injections"`
				Verification *VerificationContract `yaml:"verification"`
			} `yaml:"dependencies"`
		} `yaml:"runtime"`
		Deploy struct {
			Production struct {
				Enabled  bool     `yaml:"enabled"`
				Branches []string `yaml:"branches"`
			} `yaml:"production"`
			Preview struct {
				Enabled      bool `yaml:"enabled"`
				PullRequests bool `yaml:"pullRequests"`
			} `yaml:"preview"`
		} `yaml:"deploy"`
	} `yaml:"services"`
}

type explicitResult struct {
	Applications []Application
	Resources    []Resource
	Dependencies []Dependency
	Bindings     []Binding
	Secrets      []Secret
}

func parseExplicit(data []byte, files map[string]File) (explicitResult, error) {
	var cfg explicitConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return explicitResult{}, err
	}
	if cfg.Version != 2 || len(cfg.Services) == 0 || len(cfg.Services) > 128 {
		return explicitResult{}, errors.New("config version must be 2 and contain 1-128 services")
	}
	seen := map[string]bool{}
	result := explicitResult{}
	resourceNames := map[string]bool{}
	for _, resource := range cfg.Resources {
		if !validKey(resource.LogicalName) || resourceNames[resource.LogicalName] || resource.Type == "" {
			return explicitResult{}, fmt.Errorf("resource %q is invalid or duplicated", resource.LogicalName)
		}
		resourceNames[resource.LogicalName] = true
		evidence := Evidence{Path: ".opsi/opsi-cd.yaml", Kind: "explicit_config", Reason: "Resource intent is declared by the repository owner.", Confidence: ConfidenceHigh}
		result.Resources = append(result.Resources, Resource{LogicalName: resource.LogicalName, Type: resource.Type, Managed: resource.Managed, Required: resource.Required, Persistence: resource.Persistence, Settings: resource.Settings, Confidence: ConfidenceHigh, Reason: evidence.Reason, Evidence: []Evidence{evidence}})
	}
	for _, service := range cfg.Services {
		if !validKey(service.Key) || seen[service.Key] {
			return explicitResult{}, fmt.Errorf("service key %q is invalid or duplicated", service.Key)
		}
		seen[service.Key] = true
		root := cleanRoot(service.Build.Context)
		if !safePath(root, true) || !safePath(service.Build.Dockerfile, false) {
			return explicitResult{}, fmt.Errorf("service %s contains a non-canonical build path", service.Key)
		}
		if _, ok := files[service.Build.Dockerfile]; !ok {
			return explicitResult{}, fmt.Errorf("service %s Dockerfile does not exist at the exact commit", service.Key)
		}
		platform := service.Build.Platform
		if platform == "" {
			platform = "linux/amd64"
		}
		if platform != "linux/amd64" {
			return explicitResult{}, fmt.Errorf("service %s platform %q is unsupported", service.Key, platform)
		}
		evidence := Evidence{Path: ".opsi/opsi-cd.yaml", Kind: "explicit_config", Reason: "Service is declared by the repository owner.", Confidence: ConfidenceHigh}
		app := Application{SourceKey: service.Key, Key: service.Key, Name: service.Key, Root: root, Build: Build{Context: root, DockerfilePath: service.Build.Dockerfile, Strategy: "dockerfile", Platform: platform}, Confidence: ConfidenceHigh, Reason: evidence.Reason, Evidence: []Evidence{evidence}}
		if service.Runtime != nil {
			app.Port, app.Environment = service.Runtime.Port, service.Runtime.Environment
			if service.Runtime.Capacity != nil {
				app.Capacity = *service.Runtime.Capacity
			}
			if service.Runtime.Exposure != nil {
				app.Exposure = *service.Runtime.Exposure
			}
			for _, secret := range service.Runtime.Secrets {
				generated := secret.Source == "generated"
				reference, display := secret.Reference, "External secret reference required"
				if generated {
					reference, display = "generated://"+secret.LogicalName, "Generated and securely stored"
				}
				result.Secrets = append(result.Secrets, Secret{Name: secret.LogicalName, ApplicationKey: service.Key, EnvironmentName: secret.EnvironmentName, Generated: generated, SecretRef: reference, Display: display, Confidence: ConfidenceHigh, Reason: "Secret intent is declared without plaintext by the repository owner.", Evidence: []Evidence{evidence}})
			}
			for _, binding := range service.Runtime.Bindings {
				result.Bindings = append(result.Bindings, Binding{From: service.Key, To: binding.Target, Kind: binding.Kind, Path: binding.Path, Confidence: ConfidenceHigh, Reason: "Application binding is declared by the repository owner.", Evidence: []Evidence{evidence}})
			}
			for _, dependency := range service.Runtime.Dependencies {
				injections := make([]Injection, 0, len(dependency.Injections))
				for _, injection := range dependency.Injections {
					injections = append(injections, Injection{EnvironmentName: injection.EnvironmentName, SymbolicSource: injection.SymbolicSource, Template: injection.Template})
				}
				result.Dependencies = append(result.Dependencies, Dependency{From: service.Key, To: dependency.Target, Protocol: dependency.Protocol, Strategy: dependency.Strategy, Path: dependency.Path, Required: dependency.Required, Injections: injections, Verification: dependency.Verification, Confidence: ConfidenceHigh, Reason: "Runtime dependency is declared by the repository owner.", Evidence: []Evidence{evidence}})
			}
		}
		result.Applications = append(result.Applications, app)
	}
	return result, nil
}

type composeConfig struct {
	Services map[string]struct {
		Image       string   `yaml:"image"`
		Build       any      `yaml:"build"`
		Ports       []string `yaml:"ports"`
		DependsOn   any      `yaml:"depends_on"`
		Environment any      `yaml:"environment"`
	} `yaml:"services"`
}

func parseCompose(data []byte, composePath string, files map[string]File) ([]Application, []Resource, []Dependency, []Issue) {
	var raw struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if yaml.Unmarshal(data, &raw) != nil || len(raw.Services) == 0 {
		return nil, nil, nil, []Issue{{Code: "COMPOSE_INVALID", Message: "Compose file could not be decoded.", Path: composePath, Blocking: true}}
	}
	apps := []Application{}
	resources := []Resource{}
	deps := []Dependency{}
	issues := []Issue{}
	kafkaIssueAdded := false
	kafkaDisabled := false
	resourceNames := map[string]string{}
	resourceLogicalNames := map[string]string{}
	for name, node := range raw.Services {
		var service struct {
			Image       string    `yaml:"image"`
			Build       yaml.Node `yaml:"build"`
			Ports       []string  `yaml:"ports"`
			DependsOn   yaml.Node `yaml:"depends_on"`
			Environment yaml.Node `yaml:"environment"`
			Volumes     []string  `yaml:"volumes"`
			Healthcheck struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		}
		if node.Decode(&service) != nil {
			continue
		}
		kind := imageResourceType(service.Image)
		if kind != "" {
			logicalName := name
			if kind == "kafka" {
				logicalName = "kafka"
			}
			recommendation := "Managed " + displayResource(kind)
			if kind == "kafka" {
				recommendation = "Set Kafka__Enabled=false unless an external Kafka endpoint is supplied"
				if !kafkaIssueAdded {
					issues = append(issues, Issue{Code: "KAFKA_UNSUPPORTED", Message: "Kafka was detected but is not a managed Opsi resource.", Path: composePath, Resolution: recommendation, Blocking: true})
					kafkaIssueAdded = true
				}
			}
			evidence := Evidence{Path: composePath, Kind: "compose_image", Reason: "Compose declares image " + service.Image + ".", Confidence: ConfidenceHigh}
			var persistence *Persistence
			if (len(service.Volumes) > 0 || kind == "postgres") && kind != "kafka" {
				persistence = &Persistence{Persistent: true, SizeBytes: resourcev1.DefaultPostgresStorageBytes, PolicyRef: resourcev1.StoragePolicyDefault}
				if len(service.Volumes) > 0 {
					evidence.Reason += " A persistent volume is mounted."
				} else {
					evidence.Reason += " PostgreSQL uses persistent managed storage by default."
				}
			}
			merged := false
			for i := range resources {
				if resources[i].LogicalName == logicalName && resources[i].Type == kind {
					resources[i].Evidence = append(resources[i].Evidence, evidence)
					resources[i].Reason += " " + evidence.Reason
					merged = true
					break
				}
			}
			if !merged {
				resources = append(resources, Resource{LogicalName: logicalName, Type: kind, Managed: kind != "kafka", Required: kind != "kafka", Persistence: persistence, Recommendation: recommendation, Confidence: ConfidenceHigh, Reason: evidence.Reason, Evidence: []Evidence{evidence}})
			}
			resourceNames[name] = kind
			resourceLogicalNames[name] = logicalName
			continue
		}
		contextPath, dockerfile := composeBuild(service.Build, name)
		if dockerfile == "" {
			dockerfile = path.Join(contextPath, "Dockerfile")
		}
		strategy, image := "dockerfile", ""
		if _, ok := files[dockerfile]; !ok {
			if service.Image == "" {
				continue
			}
			strategy, dockerfile, image = "image", "", service.Image
		}
		port := firstComposePort(service.Ports)
		environment, unsafeEnvironment := composeEnvironment(service.Environment)
		kafkaDisabled = kafkaDisabled || kafkaEnvironmentDisabled(environment)
		for _, environmentName := range unsafeEnvironment {
			issues = append(issues, Issue{Code: "COMPOSE_SECRET_VALUE_AMBIGUOUS", Message: "Compose environment " + environmentName + " may contain secret material and was not copied into the plan.", Path: composePath, Resolution: "Declare a generated or external symbolic runtime secret in .opsi/opsi-cd.yaml.", Blocking: true})
		}
		evidence := Evidence{Path: composePath, Kind: "compose_service", Reason: "Compose declares a deployable application service.", Confidence: ConfidenceHigh}
		apps = append(apps, Application{SourceKey: slug(name), Key: slug(name), Name: name, Root: contextPath, Port: port, Environment: environment, Build: Build{Context: contextPath, DockerfilePath: dockerfile, Strategy: strategy, Platform: "linux/amd64", Image: image}, Confidence: ConfidenceHigh, Reason: evidence.Reason, Evidence: []Evidence{evidence}})
	}
	for name, node := range raw.Services {
		if _, isResource := resourceNames[name]; isResource {
			continue
		}
		var service struct {
			DependsOn yaml.Node `yaml:"depends_on"`
		}
		_ = node.Decode(&service)
		for _, target := range composeDependencies(service.DependsOn) {
			protocol, ok := resourceNames[target]
			if !ok {
				protocol = "http"
			}
			evidence := Evidence{Path: composePath, Kind: "compose_dependency", Reason: name + " depends on " + target + ".", Confidence: ConfidenceHigh}
			logicalTarget := target
			if resourceLogicalNames[target] != "" {
				logicalTarget = resourceLogicalNames[target]
			}
			dependency := Dependency{From: slug(name), To: logicalTarget, Protocol: protocol, Required: protocol != "http" && protocol != "kafka", Confidence: ConfidenceHigh, Reason: evidence.Reason, Evidence: []Evidence{evidence}}
			dependency.Injections = composeInjections(node, protocol)
			if contract := composeHealthVerification(node); contract != nil {
				dependency.Verification = contract
			}
			deps = append(deps, dependency)
		}
	}
	if kafkaDisabled {
		for i := range issues {
			if issues[i].Code == "KAFKA_UNSUPPORTED" {
				issues[i].Blocking = false
				issues[i].Resolution = "Kafka__Enabled=false"
			}
		}
		for i := range resources {
			if resources[i].Type == "kafka" {
				resources[i].Managed = false
				resources[i].Required = false
				resources[i].Recommendation = "Detected but disabled by Kafka__Enabled=false"
			}
		}
	}
	return apps, resources, deps, issues
}

func kafkaEnvironmentDisabled(environment map[string]string) bool {
	for name, value := range environment {
		normalized := strings.ToLower(strings.ReplaceAll(name, "_", ":"))
		if normalized == "kafka::enabled" || normalized == "kafka:enabled" {
			enabled, err := strconv.ParseBool(value)
			if err == nil && !enabled {
				return true
			}
		}
	}
	return false
}

func composeBuild(node yaml.Node, fallback string) (string, string) {
	if node.Kind == 0 {
		return ".", ""
	}
	if node.Kind == yaml.ScalarNode {
		return cleanRoot(node.Value), ""
	}
	var value struct {
		Context    string `yaml:"context"`
		Dockerfile string `yaml:"dockerfile"`
	}
	_ = node.Decode(&value)
	root := cleanRoot(value.Context)
	if value.Context == "" {
		root = "."
	}
	dockerfile := value.Dockerfile
	if dockerfile != "" && root != "." && !strings.HasPrefix(dockerfile, root+"/") {
		dockerfile = path.Join(root, dockerfile)
	}
	return root, dockerfile
}

func composeDependencies(node yaml.Node) []string {
	if node.Kind == yaml.SequenceNode {
		var values []string
		_ = node.Decode(&values)
		return values
	}
	if node.Kind == yaml.MappingNode {
		var values map[string]any
		_ = node.Decode(&values)
		out := make([]string, 0, len(values))
		for key := range values {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

func composeEnvironment(node yaml.Node) (map[string]string, []string) {
	values := map[string]string{}
	unsafe := []string{}
	if node.Kind == yaml.MappingNode {
		var raw map[string]any
		_ = node.Decode(&raw)
		for name, value := range raw {
			if deploymentv1.IsSecretLikeEnvironmentName(name) {
				unsafe = append(unsafe, name)
				continue
			}
			if value != nil {
				values[name] = fmt.Sprint(value)
			}
		}
	} else if node.Kind == yaml.SequenceNode {
		var raw []string
		_ = node.Decode(&raw)
		for _, entry := range raw {
			name, value, found := strings.Cut(entry, "=")
			if deploymentv1.IsSecretLikeEnvironmentName(name) {
				unsafe = append(unsafe, name)
				continue
			}
			if found {
				values[name] = value
			}
		}
	}
	sort.Strings(unsafe)
	if len(values) == 0 {
		values = nil
	}
	return values, unsafe
}

func composeInjections(node yaml.Node, protocol string) []Injection {
	var service struct {
		Environment yaml.Node `yaml:"environment"`
	}
	_ = node.Decode(&service)
	names := []string{}
	if service.Environment.Kind == yaml.MappingNode {
		var raw map[string]any
		_ = service.Environment.Decode(&raw)
		for name := range raw {
			names = append(names, name)
		}
	} else if service.Environment.Kind == yaml.SequenceNode {
		var raw []string
		_ = service.Environment.Decode(&raw)
		for _, entry := range raw {
			name, _, _ := strings.Cut(entry, "=")
			names = append(names, name)
		}
	}
	out := []Injection{}
	for _, name := range names {
		if source, ok := sourceForEnvironment(protocol, name); ok {
			out = append(out, Injection{EnvironmentName: name, SymbolicSource: source})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EnvironmentName < out[j].EnvironmentName })
	return out
}

var (
	healthURLPattern = regexp.MustCompile(`https?://[^/\s]+(/[^\s"']*)`)
	errBlobSizeLimit = errors.New("repository blob exceeds analysis size limit")
)

func composeHealthVerification(node yaml.Node) *VerificationContract {
	var service struct {
		Healthcheck struct {
			Test []string `yaml:"test"`
		} `yaml:"healthcheck"`
	}
	_ = node.Decode(&service)
	for _, part := range service.Healthcheck.Test {
		if match := healthURLPattern.FindStringSubmatch(part); len(match) == 2 {
			return &VerificationContract{Type: "consumer_http", Path: strings.TrimRight(match[1], ";)"), ExpectedStatus: 200}
		}
	}
	return nil
}

func mergeCompose(result *Result, apps []Application, resources []Resource, dependencies []Dependency, issues []Issue) {
	existingApps := map[string]int{}
	for i := range result.Applications {
		existingApps[result.Applications[i].SourceKey] = i
	}
	for _, detected := range apps {
		index, ok := existingApps[detected.SourceKey]
		if !ok {
			result.Applications = append(result.Applications, detected)
			existingApps[detected.SourceKey] = len(result.Applications) - 1
			continue
		}
		explicit := &result.Applications[index]
		if explicit.Build.Context != detected.Build.Context || explicit.Build.DockerfilePath != detected.Build.DockerfilePath {
			result.Issues = append(result.Issues, Issue{Code: "EXPLICIT_COMPOSE_BUILD_CONFLICT", Message: "Explicit and Compose build settings disagree for " + detected.SourceKey + ".", Path: ".opsi/opsi-cd.yaml", Resolution: "Align the explicit service build with Compose or edit the draft before approval.", Blocking: true})
		}
		if explicit.Port == 0 {
			explicit.Port = detected.Port
		} else if detected.Port != 0 && explicit.Port != detected.Port {
			result.Issues = append(result.Issues, Issue{Code: "EXPLICIT_COMPOSE_PORT_CONFLICT", Message: "Explicit and Compose ports disagree for " + detected.SourceKey + ".", Path: ".opsi/opsi-cd.yaml", Resolution: "Select the correct runtime port in the draft.", Blocking: true})
		}
		if explicit.Environment == nil {
			explicit.Environment = map[string]string{}
		}
		for name, value := range detected.Environment {
			if current, exists := explicit.Environment[name]; exists && current != value {
				result.Issues = append(result.Issues, Issue{Code: "EXPLICIT_COMPOSE_ENVIRONMENT_CONFLICT", Message: "Explicit and Compose environment values disagree for " + detected.SourceKey + " / " + name + ".", Resolution: "Select one non-secret value in the draft.", Blocking: true})
			} else if !exists {
				explicit.Environment[name] = value
			}
		}
	}
	existingResources := map[string]bool{}
	for _, resource := range result.Resources {
		existingResources[resource.LogicalName] = true
	}
	for _, resource := range resources {
		if !existingResources[resource.LogicalName] {
			result.Resources = append(result.Resources, resource)
		}
	}
	existingDependencies := map[string]bool{}
	for _, dependency := range result.Dependencies {
		existingDependencies[dependency.From+"\x00"+dependency.To+"\x00"+dependency.Protocol+"\x00"+dependency.Path] = true
	}
	for _, dependency := range dependencies {
		key := dependency.From + "\x00" + dependency.To + "\x00" + dependency.Protocol + "\x00" + dependency.Path
		if !existingDependencies[key] {
			result.Dependencies = append(result.Dependencies, dependency)
		}
	}
	result.Issues = append(result.Issues, issues...)
}

func canonicalizeResult(repository string, result *Result) {
	namespace := repositoryNamespace(repository)
	mapping, used := map[string]string{}, map[string]string{}
	for i := range result.Applications {
		application := &result.Applications[i]
		if application.SourceKey == "" {
			application.SourceKey = application.Key
		}
		canonical := canonicalApplicationKey(namespace, application.SourceKey)
		if previous, collision := used[canonical]; collision && previous != application.SourceKey {
			result.Issues = append(result.Issues, Issue{Code: "CANONICAL_KEY_COLLISION", Message: "Source keys " + previous + " and " + application.SourceKey + " produce the same canonical service key " + canonical + ".", Resolution: "Choose distinct source keys in the deployment draft.", Blocking: true})
		}
		if len(canonical) > 63 || !validKey(canonical) {
			result.Issues = append(result.Issues, Issue{Code: "CANONICAL_KEY_INVALID", Message: "Canonical service key " + canonical + " is invalid.", Resolution: "Shorten the repository or source service key.", Blocking: true})
		}
		used[canonical], mapping[application.SourceKey], application.Key = application.SourceKey, canonical, canonical
	}
	for i := range result.Dependencies {
		if value := mapping[result.Dependencies[i].From]; value != "" {
			result.Dependencies[i].From = value
		}
		if value := mapping[result.Dependencies[i].To]; value != "" {
			result.Dependencies[i].To = value
		}
	}
	for i := range result.Bindings {
		if value := mapping[result.Bindings[i].From]; value != "" {
			result.Bindings[i].From = value
		}
		if value := mapping[result.Bindings[i].To]; value != "" {
			result.Bindings[i].To = value
		}
	}
	for i := range result.Secrets {
		if value := mapping[result.Secrets[i].ApplicationKey]; value != "" {
			result.Secrets[i].ApplicationKey = value
		}
	}
}

func repositoryNamespace(repository string) string {
	base := repository
	if index := strings.LastIndex(base, "/"); index >= 0 {
		base = base[index+1:]
	}
	base = slug(strings.TrimSuffix(strings.ToLower(base), ".git"))
	base = strings.TrimSuffix(base, "-service")
	if base == "" {
		return "app"
	}
	return base
}

func canonicalApplicationKey(namespace, source string) string {
	key := slug(source)
	if key == namespace || strings.HasPrefix(key, namespace+"-") {
		return key
	}
	return namespace + "-" + key
}

func frameworkOrManifestApplications(files []File, read func(string) ([]byte, error)) []Application {
	apps := []Application{}
	seenRoots := map[string]bool{}
	for _, file := range files {
		base := strings.ToLower(path.Base(file.Path))
		if base == "package.json" || base == "go.mod" || strings.HasSuffix(base, ".csproj") || base == "requirements.txt" {
			root := path.Dir(file.Path)
			if root == "" {
				root = "."
			}
			if seenRoots[root] {
				continue
			}
			seenRoots[root] = true
			name := path.Base(root)
			if root == "." {
				name = "app"
			}
			port := 8080
			if base == "package.json" {
				port = 3000
			}
			if base == "requirements.txt" {
				port = 8000
			}
			reason := "Framework manifest defines a buildpacks-compatible application boundary."
			apps = append(apps, Application{SourceKey: slug(name), Key: slug(name), Name: name, Root: root, Port: port, Build: Build{Context: root, Strategy: "buildpack", Platform: "linux/amd64"}, Confidence: ConfidenceLow, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "framework_manifest", Reason: reason, Confidence: ConfidenceLow}}})
		}
	}
	if len(apps) > 0 {
		return apps
	}
	for _, file := range files {
		base := strings.ToLower(path.Base(file.Path))
		if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
			continue
		}
		data, err := read(file.Path)
		if err != nil {
			continue
		}
		var manifest struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `yaml:"image"`
							Ports []struct {
								ContainerPort int `yaml:"containerPort"`
							} `yaml:"ports"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal(data, &manifest) != nil || !strings.EqualFold(manifest.Kind, "Deployment") || manifest.Metadata.Name == "" || len(manifest.Spec.Template.Spec.Containers) == 0 {
			continue
		}
		container := manifest.Spec.Template.Spec.Containers[0]
		port := 0
		if len(container.Ports) > 0 {
			port = container.Ports[0].ContainerPort
		}
		reason := "Kubernetes Deployment manifest declares an immutable application image."
		root := path.Dir(file.Path)
		if root == "" {
			root = "."
		}
		apps = append(apps, Application{SourceKey: slug(manifest.Metadata.Name), Key: slug(manifest.Metadata.Name), Name: manifest.Metadata.Name, Root: root, Port: port, Build: Build{Context: root, Strategy: "image", Platform: "linux/amd64", Image: container.Image}, Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "kubernetes_manifest", Reason: reason, Confidence: ConfidenceMedium}}})
	}
	return apps
}

func dockerfileApplications(files []File, read func(string) ([]byte, error)) []Application {
	apps := []Application{}
	for _, file := range files {
		if path.Base(file.Path) != "Dockerfile" {
			continue
		}
		root := path.Dir(file.Path)
		if root == "." {
			root = "."
		}
		port := 0
		if data, err := read(file.Path); err == nil {
			port = dockerfilePort(data)
		}
		name := path.Base(root)
		if root == "." {
			name = "app"
		}
		reason := "Dockerfile defines a build boundary."
		apps = append(apps, Application{SourceKey: slug(name), Key: slug(name), Name: name, Root: root, Port: port, Build: Build{Context: root, DockerfilePath: file.Path, Strategy: "dockerfile", Platform: "linux/amd64"}, Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "dockerfile", Reason: reason, Confidence: ConfidenceMedium}}})
	}
	return apps
}

var exposePattern = regexp.MustCompile(`(?im)^\s*EXPOSE\s+([0-9]{1,5})(?:/tcp)?(?:\s|$)`)

func dockerfilePort(data []byte) int {
	match := exposePattern.FindStringSubmatch(string(data))
	if len(match) < 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	if value > 65535 {
		return 0
	}
	return value
}

func enrichApplications(result *Result, files map[string]File, read func(string) ([]byte, error)) {
	for i := range result.Applications {
		app := &result.Applications[i]
		if app.Port == 0 {
			if data, err := read(app.Build.DockerfilePath); err == nil {
				app.Port = dockerfilePort(data)
			}
		}
		if app.Port == 0 {
			for _, candidate := range []struct {
				suffix string
				port   int
			}{{".csproj", 8080}, {"package.json", 3000}, {"go.mod", 8080}, {"requirements.txt", 8000}} {
				for filePath := range files {
					if underRoot(filePath, app.Root) && (strings.HasSuffix(filePath, candidate.suffix) || path.Base(filePath) == candidate.suffix) {
						app.Port = candidate.port
						app.Evidence = append(app.Evidence, Evidence{Path: filePath, Kind: "framework_manifest", Reason: "Framework convention suggests the container port.", Confidence: ConfidenceLow})
						break
					}
				}
				if app.Port != 0 {
					break
				}
			}
		}
	}
}

func validateDetected(result *Result) {
	for _, application := range result.Applications {
		if application.Port == 0 {
			result.Issues = append(result.Issues, Issue{Code: "APPLICATION_PORT_REQUIRED", Message: "No runtime port could be determined for " + application.Key + ".", Resolution: "Set the application port in the review draft.", Blocking: true})
		}
		if application.Build.Strategy == "image" && application.Build.Image == "" {
			result.Issues = append(result.Issues, Issue{Code: "APPLICATION_IMAGE_REQUIRED", Message: "Image-based application " + application.Key + " has no immutable image reference.", Resolution: "Set an immutable image digest or select a build strategy.", Blocking: true})
		}
	}
	for dependencyIndex := range result.Dependencies {
		dependency := &result.Dependencies[dependencyIndex]
		if dependency.Protocol != serviceconfigurationv1.ProtocolHTTP && !resourcecompiler.ValidManagedProtocol(dependency.Protocol) {
			result.Issues = append(result.Issues, Issue{Code: "CONNECTION_PROTOCOL_UNSUPPORTED", Message: "Dependency " + dependency.To + " uses an unsupported managed connection protocol.", Resolution: "Select PostgreSQL, Redis/Valkey, or NATS, or use HTTP for an application dependency.", Blocking: true})
			continue
		}
		for injectionIndex := range dependency.Injections {
			injection := &dependency.Injections[injectionIndex]
			if dependency.Protocol == serviceconfigurationv1.ProtocolHTTP {
				if !resourcecompiler.ValidApplicationSource(dependency.Strategy, injection.SymbolicSource, injection.Template) {
					result.Issues = append(result.Issues, Issue{Code: "CONNECTION_MAPPING_INVALID", Message: "Injection " + injection.EnvironmentName + " uses an invalid application source.", Resolution: "Select a source valid for the HTTP dependency strategy.", Blocking: true})
				}
				continue
			}
			descriptor, err := resourcecompiler.LookupSource(dependency.Protocol, injection.SymbolicSource, injection.Template)
			if err != nil {
				result.Issues = append(result.Issues, Issue{Code: "CONNECTION_MAPPING_INVALID", Message: "Injection " + injection.EnvironmentName + " is invalid: " + err.Error(), Resolution: "Select a protocol-specific dialect or correct the safe template.", Blocking: true})
				if injection.SymbolicSource == serviceconfigurationv1.SourceConnectionTemplate {
					injection.Template = ""
				}
			} else if descriptor.Deprecated {
				result.Issues = append(result.Issues, Issue{Code: "CONNECTION_SOURCE_DEPRECATED", Message: "Injection " + injection.EnvironmentName + " uses deprecated URI alias " + injection.SymbolicSource + ".", Resolution: "Select " + resourcecompiler.CanonicalSource(dependency.Protocol, injection.SymbolicSource) + "; existing runs retain URI semantics.", Blocking: false})
				injection.SymbolicSource = resourcecompiler.CanonicalSource(dependency.Protocol, injection.SymbolicSource)
			}
		}
		if !dependency.Required {
			continue
		}
		if dependency.Protocol != "postgres" && dependency.Protocol != "redis" && dependency.Protocol != "nats" && dependency.Verification == nil {
			result.Issues = append(result.Issues, Issue{Code: "DEPENDENCY_VERIFICATION_REQUIRED", Message: "Required dependency " + dependency.From + " → " + dependency.To + " has no verification contract.", Resolution: "Add a consumer HTTP verification contract or mark the dependency optional.", Blocking: true})
		}
	}
	for _, secret := range result.Secrets {
		if !secret.Generated && secret.SecretRef == "" {
			result.Issues = append(result.Issues, Issue{Code: "EXTERNAL_SECRET_REFERENCE_REQUIRED", Message: "External secret " + secret.Name + " has not been resolved.", Resolution: "Select or upsert the scoped workload-secret reference in Review plan.", Blocking: true})
		}
	}
}
