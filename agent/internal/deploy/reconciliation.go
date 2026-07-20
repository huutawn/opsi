package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

// RoutingProbe is a trusted, bounded local Traefik probe. Implementations own
// the target boundary; arbitrary URLs are deliberately not accepted here.
type RoutingProbe interface {
	Probe(context.Context, deploymentv1.RuntimeSnapshot) (RoutingProbeResult, error)
}

type RoutingProbeResult struct {
	StatusCode   int
	EvidenceHash string
}

// BoundedHTTPProbe is a local trusted probe for disposable/Agent-local
// Traefik routing. It accepts only an explicit loopback address and canonical
// ExposureSpec host/path; redirects, metadata addresses, and Unix sockets are
// rejected.
type BoundedHTTPProbe struct {
	Scheme  string
	Address string
	Port    int
	Timeout time.Duration
	MaxBody int64
}

func (p BoundedHTTPProbe) Probe(ctx context.Context, snapshot deploymentv1.RuntimeSnapshot) (RoutingProbeResult, error) {
	if p.Scheme != "http" && p.Scheme != "https" {
		return RoutingProbeResult{}, errors.New("probe scheme is not allowed")
	}
	if net.ParseIP(p.Address) == nil || !isLoopbackProbeAddress(p.Address) || p.Port < 1 || p.Port > 65535 {
		return RoutingProbeResult{}, errors.New("probe address is outside the trusted boundary")
	}
	if p.Timeout <= 0 {
		p.Timeout = 5 * time.Second
	}
	if p.MaxBody <= 0 || p.MaxBody > 64*1024 {
		p.MaxBody = 16 * 1024
	}
	pathValue, err := exposurev1.NormalizePath(snapshot.Exposure.Path)
	if err != nil {
		return RoutingProbeResult{}, err
	}
	urlValue := url.URL{Scheme: p.Scheme, Host: net.JoinHostPort(p.Address, strconv.Itoa(p.Port)), Path: pathValue}
	requestCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, urlValue.String(), nil)
	if err != nil {
		return RoutingProbeResult{}, errors.New("probe request could not be created")
	}
	request.Host = snapshot.Exposure.Hostname
	client := &http.Client{Timeout: p.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return RoutingProbeResult{}, errors.New("probe request failed")
	}
	defer response.Body.Close()
	readBytes, _ := io.CopyN(io.Discard, response.Body, p.MaxBody+1)
	if response.ContentLength > p.MaxBody || readBytes > p.MaxBody {
		return RoutingProbeResult{}, errors.New("probe response exceeded the allowed bound")
	}
	evidence := hashValue(map[string]any{"status": response.StatusCode, "host": snapshot.Exposure.Hostname, "path": pathValue})
	return RoutingProbeResult{StatusCode: response.StatusCode, EvidenceHash: evidence}, nil
}

func isLoopbackProbeAddress(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.IsLoopback()
}

type RolloutRuntime interface {
	PrepareRollout(context.Context, deploymentv1.RuntimeSnapshot) (RolloutPlan, error)
	ApplyRollout(context.Context, RolloutPlan) ([]deploymentv1.ResourceIdentity, error)
	ObserveReadiness(context.Context, RolloutPlan) (deploymentv1.ReadinessEvidence, []deploymentv1.ResourceIdentity, error)
}

type RolloutPlan struct {
	Snapshot       deploymentv1.RuntimeSnapshot
	Command        deploymentv1.AgentCommand
	Resources      renderedResources
	Exposure       RenderedExposure
	DesiredObjects []rolloutObject
	Observed       []rolloutObservation
}

type rolloutObject struct {
	Kind       string
	Namespace  string
	Name       string
	Manager    string
	Object     map[string]any
	Functional string
}

type rolloutObservation struct {
	rolloutObject
	Exists          bool
	UID             string
	ResourceVersion string
}

func (a ProductionAdapter) PrepareRollout(ctx context.Context, snapshot deploymentv1.RuntimeSnapshot) (RolloutPlan, error) {
	if err := snapshot.Validate(); err != nil {
		return RolloutPlan{}, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeInvalid, err.Error(), false)
	}
	a = a.withDefaults()
	command := snapshot.AgentCommand()
	_, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		return RolloutPlan{}, deploymentv1.NewRolloutError("WORKLOAD_RENDER_FAILED", err.Error(), false)
	}
	exposure, err := renderExposure(ctx, command, snapshot.Exposure, a.TLSResolver)
	if err != nil {
		return RolloutPlan{}, err
	}
	objects, err := rolloutObjects(resources, exposure)
	if err != nil {
		return RolloutPlan{}, err
	}
	plan := RolloutPlan{Snapshot: snapshot, Command: command, Resources: resources, Exposure: exposure, DesiredObjects: objects}
	for index := range plan.DesiredObjects {
		observation, err := a.observeRolloutObject(ctx, plan.DesiredObjects[index])
		if err != nil {
			return RolloutPlan{}, err
		}
		plan.Observed = append(plan.Observed, observation)
		if observation.Exists {
			if err := a.verifyRolloutOwnership(observation.Object, plan.DesiredObjects[index], snapshot); err != nil {
				return RolloutPlan{}, err
			}
		}
	}
	// Exposure route conflicts are checked even when the workload Service is
	// absent: first rollout must be allowed to create both resources together.
	if serviceObservation := plan.Observed[2]; serviceObservation.Exists {
		if _, err := a.PreflightExposure(ctx, command, snapshot.Exposure, a.TLSResolver); err != nil {
			return RolloutPlan{}, err
		}
	} else {
		all, err := a.listIngresses(ctx)
		if err != nil {
			return RolloutPlan{}, err
		}
		if err := preflightRouteConflicts(all, snapshot.Exposure, exposure); err != nil {
			return RolloutPlan{}, err
		}
	}
	_ = namespace
	return plan, nil
}

func (a ProductionAdapter) ApplyRollout(ctx context.Context, plan RolloutPlan) ([]deploymentv1.ResourceIdentity, error) {
	a = a.withDefaults()
	if len(plan.DesiredObjects) != len(plan.Observed) || len(plan.DesiredObjects) == 0 {
		return nil, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeInvalid, "rollout plan observations are incomplete", false)
	}
	var result []deploymentv1.ResourceIdentity
	for index, desired := range plan.DesiredObjects {
		current, err := a.observeRolloutObject(ctx, desired)
		if err != nil {
			return nil, err
		}
		observed := plan.Observed[index]
		if current.Exists != observed.Exists || current.UID != observed.UID || current.ResourceVersion != observed.ResourceVersion || current.Functional != observed.Functional {
			return nil, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeResourceChanged, desired.Kind+"/"+desired.Name+" changed after preflight", false)
		}
		if current.Exists {
			if err := a.verifyRolloutOwnership(current.Object, desired, plan.Snapshot); err != nil {
				return nil, err
			}
			if desired.Kind == "Namespace" {
				result = append(result, resourceIdentity(current))
				continue
			}
		}
		manifest := cloneMap(desired.Object)
		metadata, _ := manifest["metadata"].(map[string]any)
		verb := "create"
		if current.Exists {
			verb = "replace"
			preserveMetadata(current.Object, manifest)
			if desired.Kind == "Service" {
				preserveServiceSpec(current.Object, manifest)
			}
			metadata["uid"] = current.UID
			metadata["resourceVersion"] = current.ResourceVersion
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		args := []string{verb, "--field-manager=" + desired.Manager, "-f", "-"}
		if _, err := a.Runner.Run(ctx, data, a.KubectlPath, args...); err != nil {
			return nil, deploymentv1.NewRolloutError("K8S_APPLY_FAILED", RedactSensitive(err.Error()), true)
		}
		final, err := a.observeRolloutObject(ctx, desired)
		if err != nil {
			return nil, err
		}
		if !final.Exists || final.UID == "" || final.ResourceVersion == "" {
			return nil, deploymentv1.NewRolloutError("K8S_POST_APPLY_READ_FAILED", "resource identity was not returned after apply", true)
		}
		result = append(result, resourceIdentity(final))
	}
	return result, nil
}

func preserveMetadata(current, desired map[string]any) {
	currentMetadata, _ := current["metadata"].(map[string]any)
	desiredMetadata, _ := desired["metadata"].(map[string]any)
	for _, field := range []string{"labels", "annotations"} {
		currentValues := stringMap(currentMetadata[field])
		desiredValues := stringMap(desiredMetadata[field])
		for key, value := range currentValues {
			if _, exists := desiredValues[key]; !exists {
				desiredValues[key] = value
			}
		}
		desiredMetadata[field] = desiredValues
	}
}

func preserveServiceSpec(current, desired map[string]any) {
	currentSpec, _ := current["spec"].(map[string]any)
	desiredSpec, _ := desired["spec"].(map[string]any)
	for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "internalTrafficPolicy", "healthCheckNodePort", "sessionAffinity", "sessionAffinityConfig"} {
		if value, exists := currentSpec[key]; exists {
			desiredSpec[key] = value
		}
	}
}

func (a ProductionAdapter) ObserveReadiness(ctx context.Context, plan RolloutPlan) (deploymentv1.ReadinessEvidence, []deploymentv1.ResourceIdentity, error) {
	a = a.withDefaults()
	deadline := time.NewTimer(a.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(a.PollInterval)
	defer ticker.Stop()
	for {
		evidence, resources, ready, err := a.readinessOnce(ctx, plan)
		if err != nil {
			var failure *deploymentv1.RolloutError
			if errors.As(err, &failure) && !failure.Retryable {
				return deploymentv1.ReadinessEvidence{}, nil, err
			}
			ready = false
		}
		if ready {
			return evidence, resources, nil
		}
		select {
		case <-ctx.Done():
			return deploymentv1.ReadinessEvidence{}, nil, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, "readiness context cancelled", true)
		case <-deadline.C:
			return deploymentv1.ReadinessEvidence{}, nil, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, "runtime readiness timed out", true)
		case <-ticker.C:
		}
	}
}

func (a ProductionAdapter) readinessOnce(ctx context.Context, plan RolloutPlan) (deploymentv1.ReadinessEvidence, []deploymentv1.ResourceIdentity, bool, error) {
	command := plan.Command
	deployment, err := a.getJSON(ctx, "deployment", plan.Resources.DeploymentName, plan.Resources.Namespace)
	if err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, false, err
	}
	service, err := a.getJSON(ctx, "service", plan.Resources.ServiceName, plan.Resources.Namespace)
	if err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, false, err
	}
	endpoints, err := a.getJSON(ctx, "endpoints", plan.Resources.ServiceName, plan.Resources.Namespace)
	if err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, false, err
	}
	ingress, err := a.getJSON(ctx, "ingress", plan.Exposure.IngressName, plan.Exposure.Namespace)
	if err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, false, err
	}
	pods, err := a.getJSON(ctx, "pods", "", plan.Resources.Namespace, plan.Resources.Selector)
	if err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, false, err
	}
	metadata, _ := deployment["metadata"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	generation := number(metadata["generation"])
	observedGeneration := number(status["observedGeneration"])
	desiredReplicas := number(deploymentJSONNested(deployment, "spec", "replicas"))
	if desiredReplicas == 0 {
		desiredReplicas = int(command.Workload.Replicas)
	}
	available := number(status["availableReplicas"])
	imageID, readyCount := applicationPodReadiness(pods, command.Image.Digest)
	workloadReady := generation > 0 && observedGeneration >= generation && available >= desiredReplicas && readyCount >= desiredReplicas && imageID != "" && deploymentHasExactAppImage(deployment, command.Image.Reference)
	serviceReady := ownedWorkloadObject(service, plan.Resources.Service) && serviceObjectHasExactPort(service, command.Workload.ContainerPort) && equalLogical(serviceJSON(service, "selector"), plan.Resources.Selector)
	endpointReady := endpointsReady(endpoints, int(command.Workload.ContainerPort), desiredReplicas)
	ingressReady := ownedExposureIdentity(ingress, plan.Snapshot.Exposure, plan.Exposure.IngressName) && ingressGenerationReady(ingress) && ingressMatches(ingress, plan.Exposure.Ingress)
	runtimeReady := workloadReady && serviceReady && endpointReady && ingressReady
	evidence := deploymentv1.ReadinessEvidence{SchemaVersion: deploymentv1.ReadinessEvidenceVersion, RuntimeReady: runtimeReady, LocalRoutingReady: !a.RequireLocalRouting, ExternalReady: false, ObservedAt: time.Now().UTC()}
	evidence.WorkloadEvidenceHash = hashValue(map[string]any{"generation": generation, "observed": observedGeneration, "available": available, "desired": desiredReplicas})
	evidence.ServiceEvidenceHash = hashValue(map[string]any{"selector": serviceJSON(service, "selector"), "port": command.Workload.ContainerPort, "endpoints": endpointReady})
	evidence.ExposureEvidenceHash = hashValue(map[string]any{"generation": number(metadataValue(ingress, "generation")), "spec": ingress["spec"], "ownership": ingressReady})
	evidence.ApplicationImageIDHash = hashString(imageID)
	resources := []deploymentv1.ResourceIdentity{resourceIdentityFromObject("Deployment", deployment), resourceIdentityFromObject("Service", service), resourceIdentityFromObject("Ingress", ingress)}
	if a.RequireLocalRouting {
		if a.RoutingProbe == nil {
			return evidence, resources, false, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeExternalUnavailable, "local routing probe is not configured", false)
		}
		probe, err := a.RoutingProbe.Probe(ctx, plan.Snapshot)
		if err != nil {
			return evidence, resources, false, nil
		}
		evidence.LocalRoutingReady = probe.StatusCode >= 200 && probe.StatusCode < 400 && rolloutHash(probe.EvidenceHash)
		evidence.LocalProbeEvidenceHash = probe.EvidenceHash
	}
	return evidence, resources, runtimeReady && evidence.LocalRoutingReady, nil
}

func (a ProductionAdapter) observeRolloutObject(ctx context.Context, object rolloutObject) (rolloutObservation, error) {
	args := []string{"get", strings.ToLower(object.Kind), object.Name}
	if object.Namespace != "" {
		args = append(args, "-n", object.Namespace)
	}
	args = append(args, "-o", "json", "--ignore-not-found", "--show-managed-fields")
	readCtx, cancel := context.WithTimeout(ctx, a.kubernetesReadTimeout())
	defer cancel()
	out, err := a.Runner.Run(readCtx, nil, a.KubectlPath, args...)
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return rolloutObservation{}, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, "Kubernetes read timed out", true)
		}
		return rolloutObservation{}, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "Kubernetes ownership read failed", false)
	}
	if len(out) > a.kubernetesOutputLimit() {
		return rolloutObservation{}, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "Kubernetes response exceeded the bound", false)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return rolloutObservation{rolloutObject: object, Exists: false}, nil
	}
	var current map[string]any
	if err := decodeSingleJSON(out, &current); err != nil {
		return rolloutObservation{}, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "Kubernetes response was invalid", false)
	}
	metadata, _ := current["metadata"].(map[string]any)
	uid, _ := metadata["uid"].(string)
	resourceVersion, _ := metadata["resourceVersion"].(string)
	observed := object
	observed.Object = current
	observed.Functional = objectFunctionalHash(current)
	return rolloutObservation{rolloutObject: observed, Exists: true, UID: uid, ResourceVersion: resourceVersion}, nil
}

func (a ProductionAdapter) verifyRolloutOwnership(current map[string]any, desired rolloutObject, snapshot deploymentv1.RuntimeSnapshot) error {
	switch desired.Kind {
	case "Namespace":
		metadata, _ := current["metadata"].(map[string]any)
		labels := stringMap(metadata["labels"])
		if labels["app.kubernetes.io/managed-by"] != "opsi" || labels["opsi.dev/project"] != safeLabel(snapshot.Target.ProjectID) || labels["opsi.dev/environment"] != safeLabel(snapshot.Target.EnvironmentID) {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "existing Namespace is not Opsi-owned", false)
		}
	case "Deployment", "Service":
		if !ownedWorkloadObject(current, desired.Object) || hasUnsupportedWorkloadAnnotations(current) || hasForeignSpecManager(current, desired.Manager) {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "existing workload resource is not safely Opsi-owned", false)
		}
	case "Ingress":
		if !ownedExposureIdentity(current, snapshot.Exposure, desired.Name) || hasUnsupportedIngressAnnotations(current) || hasForeignSpecManager(current, desired.Manager) {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "existing Ingress is not safely Opsi-owned", false)
		}
	}
	return nil
}

func hasUnsupportedWorkloadAnnotations(object map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)
	for key := range stringMap(metadata["annotations"]) {
		switch key {
		case "opsi.dev/spec-hash", "opsi.dev/image-digest", "deployment.kubernetes.io/revision":
		default:
			return true
		}
	}
	return false
}

func hasForeignSpecManager(object map[string]any, allowed string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	managed, _ := metadata["managedFields"].([]any)
	for _, raw := range managed {
		entry, _ := raw.(map[string]any)
		manager, _ := entry["manager"].(string)
		fields, _ := entry["fieldsV1"].(map[string]any)
		if manager != "" && manager != allowed {
			if _, ownsSpec := fields["f:spec"]; ownsSpec {
				return true
			}
		}
	}
	return false
}

func rolloutObjects(resources renderedResources, exposure RenderedExposure) ([]rolloutObject, error) {
	objects := []rolloutObject{
		{Kind: "Namespace", Name: resources.Namespace, Manager: ProductionFieldManager, Object: resources.NamespaceObject},
		{Kind: "Deployment", Namespace: resources.Namespace, Name: resources.DeploymentName, Manager: ProductionFieldManager, Object: resources.Deployment},
		{Kind: "Service", Namespace: resources.Namespace, Name: resources.ServiceName, Manager: ProductionFieldManager, Object: resources.Service},
		{Kind: "Ingress", Namespace: exposure.Namespace, Name: exposure.IngressName, Manager: exposure.FieldManager, Object: exposure.Ingress},
	}
	for index := range objects {
		objects[index].Functional = objectFunctionalHash(objects[index].Object)
	}
	return objects, nil
}

func resourceIdentity(observation rolloutObservation) deploymentv1.ResourceIdentity {
	return deploymentv1.ResourceIdentity{Kind: observation.Kind, Namespace: observation.Namespace, Name: observation.Name, UID: observation.UID, ResourceVersion: observation.ResourceVersion, FunctionalHash: observation.Functional}
}

func resourceIdentityFromObject(kind string, object map[string]any) deploymentv1.ResourceIdentity {
	metadata, _ := object["metadata"].(map[string]any)
	uid, _ := metadata["uid"].(string)
	rv, _ := metadata["resourceVersion"].(string)
	namespace, _ := metadata["namespace"].(string)
	name, _ := metadata["name"].(string)
	return deploymentv1.ResourceIdentity{Kind: kind, Namespace: namespace, Name: name, UID: uid, ResourceVersion: rv, FunctionalHash: objectFunctionalHash(object)}
}

func objectFunctionalHash(object map[string]any) string {
	kind, _ := object["kind"].(string)
	if kind == "Service" {
		return serviceFunctionalHash(object)
	}
	if _, exists := object["spec"]; exists {
		return hashValue(object["spec"])
	}
	metadata, _ := object["metadata"].(map[string]any)
	return hashValue(metadata["labels"])
}

func cloneMap(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func (a ProductionAdapter) withDefaults() ProductionAdapter {
	if a.Runner == nil {
		a.Runner = ExecCommandRunner{}
	}
	if a.KubectlPath == "" {
		a.KubectlPath = "kubectl"
	}
	if a.Timeout <= 0 {
		a.Timeout = 10 * time.Minute
	}
	if a.PollInterval <= 0 {
		a.PollInterval = time.Second
	}
	// R5-011.2 requires a factual local routing gate. Public readiness is
	// intentionally not inferred when no trusted probe exists.
	a.RequireLocalRouting = true
	return a
}

func deploymentHasExactAppImage(object map[string]any, image string) bool {
	containers, _ := deploymentJSONNested(object, "spec", "template").(map[string]any)
	podSpec, _ := containers["spec"].(map[string]any)
	items, _ := podSpec["containers"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		name, _ := item["name"].(string)
		value, _ := item["image"].(string)
		if name == deploymentv1.ApplicationContainer {
			return value == image
		}
	}
	return false
}

func endpointsReady(object map[string]any, port, replicas int) bool {
	subsets, _ := object["subsets"].([]any)
	addresses := 0
	for _, raw := range subsets {
		subset, _ := raw.(map[string]any)
		items, _ := subset["addresses"].([]any)
		ports, _ := subset["ports"].([]any)
		portMatch := false
		for _, rawPort := range ports {
			item, _ := rawPort.(map[string]any)
			if number(item["port"]) == port {
				portMatch = true
			}
		}
		if portMatch {
			addresses += len(items)
		}
	}
	return addresses >= replicas
}

func ingressGenerationReady(object map[string]any) bool {
	return number(metadataValue(object, "generation")) > 0
}

func ingressMatches(current, desired map[string]any) bool {
	diff, err := ingressDiff(current, desired)
	return err == nil && len(diff) == 0
}

func serviceJSON(object map[string]any, key string) any {
	spec, _ := object["spec"].(map[string]any)
	return spec[key]
}

func equalLogical(left, right any) bool { return hashValue(left) == hashValue(right) }

func metadataValue(object map[string]any, key string) any {
	metadata, _ := object["metadata"].(map[string]any)
	return metadata[key]
}

func hashValue(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashString(value string) string { return hashValue(value) }

func rolloutHash(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
