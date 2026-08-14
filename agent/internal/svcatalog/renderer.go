package svcatalog

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed manifests/external.yaml.tmpl
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

func ManagedSupported(serviceType string) bool {
	return false
}
