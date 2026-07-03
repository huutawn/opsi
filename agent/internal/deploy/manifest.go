package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifestOptions struct {
	ResourceRequestsJSON          string
	ResourceLimitsJSON            string
	TerminationGracePeriodSeconds int
	IngressEnabled                bool
	BindingDependencies           []ServiceDependency
}

func renderManifestFile(sourcePath, outputPath string, options manifestOptions) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	rendered, err := renderManifest(data, options)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, rendered, 0o600); err != nil {
		return fmt.Errorf("write rendered manifest: %w", err)
	}
	return nil
}

func renderManifest(data []byte, options manifestOptions) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var docs []map[string]any
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		if len(doc) == 0 {
			continue
		}
		injectDeploymentDefaults(doc, options)
		docs = append(docs, doc)
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	for i, doc := range docs {
		if i > 0 {
			out.WriteString("---\n")
		}
		if err := encoder.Encode(doc); err != nil {
			_ = encoder.Close()
			return nil, fmt.Errorf("encode manifest: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close manifest encoder: %w", err)
	}
	return out.Bytes(), nil
}

func injectDeploymentDefaults(doc map[string]any, options manifestOptions) {
	if doc["kind"] != "Deployment" {
		return
	}
	spec := ensureMap(doc, "spec")
	strategy := ensureMap(spec, "strategy")
	strategy["type"] = "RollingUpdate"
	rolling := ensureMap(strategy, "rollingUpdate")
	rolling["maxUnavailable"] = 0
	rolling["maxSurge"] = 1

	template := ensureMap(spec, "template")
	bindings := bindingRefs(options.BindingDependencies)
	if len(bindings) > 0 {
		metadata := ensureMap(template, "metadata")
		annotations := ensureMap(metadata, "annotations")
		annotations["opsi.io/bindings-checksum"] = bindingsChecksum(bindings)
	}
	podSpec := ensureMap(template, "spec")
	if options.TerminationGracePeriodSeconds <= 0 {
		options.TerminationGracePeriodSeconds = DefaultTerminationGracePeriodSeconds
	}
	podSpec["terminationGracePeriodSeconds"] = options.TerminationGracePeriodSeconds

	containers, _ := podSpec["containers"].([]any)
	requests := resourceMap(options.ResourceRequestsJSON, DefaultResourceRequestsJSON)
	limits := resourceMap(options.ResourceLimitsJSON, DefaultResourceLimitsJSON)
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		resources := ensureMap(container, "resources")
		mergeStringMap(ensureMap(resources, "requests"), requests)
		mergeStringMap(ensureMap(resources, "limits"), limits)
		if options.IngressEnabled {
			lifecycle := ensureMap(container, "lifecycle")
			preStop := ensureMap(lifecycle, "preStop")
			exec := ensureMap(preStop, "exec")
			exec["command"] = []any{"sh", "-c", "sleep 10"}
		}
		appendBindings(container, bindings)
	}
}

type bindingRef struct {
	EnvName    string `json:"env_name"`
	SecretName string `json:"secret_name"`
	SecretKey  string `json:"secret_key"`
}

func appendBindings(container map[string]any, bindings []bindingRef) {
	if len(bindings) == 0 {
		return
	}
	env, _ := container["env"].([]any)
	seen := map[string]bool{}
	for _, raw := range env {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := item["name"].(string); name != "" {
			seen[name] = true
		}
	}
	for _, binding := range bindings {
		if seen[binding.EnvName] {
			continue
		}
		env = append(env, map[string]any{
			"name": binding.EnvName,
			"valueFrom": map[string]any{"secretKeyRef": map[string]any{
				"name": binding.SecretName,
				"key":  binding.SecretKey,
			}},
		})
	}
	container["env"] = env
}

func bindingRefs(deps []ServiceDependency) []bindingRef {
	if len(deps) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, dep := range deps {
		for _, key := range dep.EnvKeys {
			counts[key]++
		}
	}
	var refs []bindingRef
	for _, dep := range deps {
		secret := "opsi-svc-" + dep.Name
		servicePrefix := "OPSI_" + envNamePart(dep.Name) + "_"
		for _, key := range dep.EnvKeys {
			refs = append(refs, bindingRef{EnvName: servicePrefix + key, SecretName: secret, SecretKey: servicePrefix + key})
			if dep.EnvPrefix != "" {
				refs = append(refs, bindingRef{EnvName: dep.EnvPrefix + "_" + key, SecretName: secret, SecretKey: key})
			}
			if dep.ExposeAsDefault || (dep.EnvPrefix == "" && counts[key] == 1) {
				refs = append(refs, bindingRef{EnvName: key, SecretName: secret, SecretKey: key})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].EnvName != refs[j].EnvName {
			return refs[i].EnvName < refs[j].EnvName
		}
		if refs[i].SecretName != refs[j].SecretName {
			return refs[i].SecretName < refs[j].SecretName
		}
		return refs[i].SecretKey < refs[j].SecretKey
	})
	return refs
}

func bindingsChecksum(bindings []bindingRef) string {
	data, _ := json.Marshal(bindings)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func envNamePart(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func resourceMap(value, fallback string) map[string]string {
	var parsed map[string]string
	if err := json.Unmarshal([]byte(value), &parsed); err != nil || len(parsed) == 0 {
		_ = json.Unmarshal([]byte(fallback), &parsed)
	}
	return parsed
}

func mergeStringMap(dst map[string]any, src map[string]string) {
	for key, value := range src {
		if _, exists := dst[key]; !exists {
			dst[key] = value
		}
	}
}
