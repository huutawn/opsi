package svcatalog

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed manifests/*.yaml.tmpl
var manifestTemplates embed.FS

type RenderRequest struct {
	ProjectID string
	Name      string
	Type      string
	Namespace string
	Overrides map[string]string
}

type RenderedManifest struct {
	Service ManagedService
	Binding RenderedService
	YAML    []byte
}

func RenderManaged(req RenderRequest) (RenderedManifest, error) {
	return RenderManagedWithReader(req, nil)
}

func RenderManagedWithReader(req RenderRequest, reader io.Reader) (RenderedManifest, error) {
	if err := validateRenderRequest(req); err != nil {
		return RenderedManifest{}, err
	}
	namespace := defaultString(req.Namespace, "default")
	overrides := copyMap(req.Overrides)
	overrides["host"] = defaultString(overrides["host"], req.Name+"."+namespace+".svc.cluster.local")

	schema, ok := BuiltInCatalog().Get(req.Type)
	if !ok {
		return RenderedManifest{}, fmt.Errorf("unknown service type %q", req.Type)
	}
	if !rendererSupported(schema.Type) {
		return RenderedManifest{}, fmt.Errorf("managed renderer for %s is not implemented", schema.Type)
	}
	binding, err := renderSchema(schema, overrides, reader)
	if err != nil {
		return RenderedManifest{}, err
	}
	binding.Env = scopedEnv(req.Name, binding.Env)

	service := ManagedService{
		ID:            req.Name,
		ProjectID:     req.ProjectID,
		Name:          req.Name,
		Type:          schema.Type,
		Namespace:     namespace,
		Mode:          "managed",
		Status:        "created",
		Host:          binding.Config["host"],
		Port:          binding.Config["port"],
		Version:       binding.Config["version"],
		Config:        binding.Config,
		SecretName:    "opsi-svc-" + req.Name,
		ConfigMapName: "opsi-bind-" + req.Name,
	}
	data := manifestData{Service: service, Env: binding.Env}
	rendered, err := executeManifestTemplate(schema.Type, data)
	if err != nil {
		return RenderedManifest{}, err
	}
	if err := validateManifestYAML(rendered); err != nil {
		return RenderedManifest{}, err
	}
	return RenderedManifest{Service: service, Binding: binding, YAML: rendered}, nil
}

type manifestData struct {
	Service ManagedService
	Env     map[string]string
}

func renderSchema(schema ServiceSchema, overrides map[string]string, reader io.Reader) (RenderedService, error) {
	if reader == nil {
		return schema.Render(overrides)
	}
	return schema.RenderWithReader(overrides, reader)
}

func executeManifestTemplate(serviceType string, data manifestData) ([]byte, error) {
	name := "manifests/" + serviceType + ".yaml.tmpl"
	tmpl, err := template.ParseFS(manifestTemplates, name)
	if err != nil {
		return nil, fmt.Errorf("parse %s manifest template: %w", serviceType, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("render %s manifest: %w", serviceType, err)
	}
	return out.Bytes(), nil
}

func validateManifestYAML(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode rendered manifest: %w", err)
		}
		if len(doc) == 0 {
			continue
		}
		if doc["apiVersion"] == "" || doc["kind"] == "" {
			return fmt.Errorf("rendered manifest document is missing apiVersion/kind")
		}
	}
}

func scopedEnv(serviceName string, env map[string]string) map[string]string {
	out := copyMap(env)
	prefix := "OPSI_" + envPrefix(serviceName) + "_"
	for key, value := range env {
		out[prefix+key] = value
	}
	return out
}

func envPrefix(name string) string {
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

func copyMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func rendererSupported(serviceType string) bool {
	return serviceType == "postgresql" || serviceType == "redis"
}

func ManagedSupported(serviceType string) bool {
	return rendererSupported(normalizeType(serviceType))
}
