package svcatalog

import (
	"bytes"
	"fmt"
	"net"
	"text/template"
)

type RegisterExternalRequest struct {
	ProjectID string
	Name      string
	Type      string
	Namespace string
	Host      string
	Port      string
	Overrides map[string]string
}

func RenderExternal(req RegisterExternalRequest) (RenderedManifest, error) {
	if err := validateRenderRequest(RenderRequest{ProjectID: req.ProjectID, Name: req.Name, Type: req.Type, Namespace: req.Namespace}); err != nil {
		return RenderedManifest{}, err
	}
	if req.Host == "" {
		return RenderedManifest{}, fmt.Errorf("external host is required")
	}
	namespace := defaultString(req.Namespace, "default")
	schema, ok := BuiltInCatalog().Get(req.Type)
	if !ok {
		return RenderedManifest{}, fmt.Errorf("unknown service type %q", req.Type)
	}
	config := map[string]string{}
	for _, key := range schema.ConfigKeys {
		config[key.Key] = key.Default
	}
	for key, value := range req.Overrides {
		if key == "password" {
			continue
		}
		if _, ok := config[key]; !ok {
			return RenderedManifest{}, fmt.Errorf("unknown config key %q for %s", key, schema.Type)
		}
		config[key] = value
	}
	config["host"] = req.Name + "." + namespace + ".svc.cluster.local"
	config["port"] = defaultString(req.Port, config["port"])
	for _, key := range schema.ConfigKeys {
		if err := validateConfigValue(key, config[key.Key]); err != nil {
			return RenderedManifest{}, err
		}
	}
	secrets := map[string]string{}
	for _, key := range schema.SecretKeys {
		value := req.Overrides[key.Key]
		if value == "" && key.Required {
			return RenderedManifest{}, fmt.Errorf("secret key %q is required for external %s", key.Key, schema.Type)
		}
		if value != "" {
			secrets[key.Key] = value
		}
	}
	env := scopedEnv(req.Name, renderEnv(schema.EnvMapping, config, secrets))
	service := ManagedService{
		ID:            req.Name,
		ProjectID:     req.ProjectID,
		Name:          req.Name,
		Type:          schema.Type,
		Namespace:     namespace,
		Mode:          "external",
		Status:        "registered",
		Host:          config["host"],
		Port:          config["port"],
		Version:       config["version"],
		Config:        config,
		SecretName:    "opsi-svc-" + req.Name,
		ConfigMapName: "opsi-bind-" + req.Name,
	}
	data := externalManifestData{Service: service, Env: env, TargetHost: req.Host, TargetIP: net.ParseIP(req.Host) != nil}
	rendered, err := executeExternalTemplate(data)
	if err != nil {
		return RenderedManifest{}, err
	}
	if err := validateManifestYAML(rendered); err != nil {
		return RenderedManifest{}, err
	}
	return RenderedManifest{Service: service, Binding: RenderedService{Schema: schema, Config: config, Secrets: secrets, Env: env}, YAML: rendered}, nil
}

type externalManifestData struct {
	Service    ManagedService
	Env        map[string]string
	TargetHost string
	TargetIP   bool
}

func renderEnv(mapping map[string]EnvTemplate, config, secrets map[string]string) map[string]string {
	data := envData(config, secrets)
	env := map[string]string{}
	for key, tmpl := range mapping {
		env[key] = renderTemplate(string(tmpl), data)
	}
	return env
}

func executeExternalTemplate(data externalManifestData) ([]byte, error) {
	tmpl, err := template.ParseFS(manifestTemplates, "manifests/external.yaml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse external manifest template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("render external manifest: %w", err)
	}
	return out.Bytes(), nil
}
