package svcatalog

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const secretAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

type ServiceSchema struct {
	Type        string
	DisplayName string
	ConfigKeys  []ConfigKey
	SecretKeys  []SecretKey
	EnvMapping  map[string]EnvTemplate
}

type ConfigKey struct {
	Key      string
	Default  string
	Required bool
	Enum     []string
	Pattern  string
}

type SecretKey struct {
	Key          string
	AutoGenerate bool
	Length       int
	Required     bool
}

type EnvTemplate string

type RenderedService struct {
	Schema  ServiceSchema
	Config  map[string]string
	Secrets map[string]string
	Env     map[string]string
}

func (s ServiceSchema) Render(overrides map[string]string) (RenderedService, error) {
	return s.RenderWithReader(overrides, rand.Reader)
}

func (s ServiceSchema) RenderWithReader(overrides map[string]string, reader io.Reader) (RenderedService, error) {
	if err := s.Validate(); err != nil {
		return RenderedService{}, err
	}
	config := map[string]string{}
	allowed := map[string]ConfigKey{}
	for _, key := range s.ConfigKeys {
		config[key.Key] = key.Default
		allowed[key.Key] = key
	}
	for key, value := range overrides {
		spec, ok := allowed[key]
		if !ok {
			return RenderedService{}, fmt.Errorf("unknown config key %q for %s", key, s.Type)
		}
		config[key] = value
		if err := validateConfigValue(spec, value); err != nil {
			return RenderedService{}, err
		}
	}
	for _, spec := range s.ConfigKeys {
		if spec.Required && config[spec.Key] == "" {
			return RenderedService{}, fmt.Errorf("config key %q is required for %s", spec.Key, s.Type)
		}
		if err := validateConfigValue(spec, config[spec.Key]); err != nil {
			return RenderedService{}, err
		}
	}
	secrets := map[string]string{}
	for _, spec := range s.SecretKeys {
		if spec.AutoGenerate {
			value, err := randomString(reader, spec.Length)
			if err != nil {
				return RenderedService{}, fmt.Errorf("generate %s secret: %w", spec.Key, err)
			}
			secrets[spec.Key] = value
		}
		if spec.Required && secrets[spec.Key] == "" {
			return RenderedService{}, fmt.Errorf("secret key %q is required for %s", spec.Key, s.Type)
		}
	}
	envData := envData(config, secrets)
	env := map[string]string{}
	for key, tmpl := range s.EnvMapping {
		env[key] = renderTemplate(string(tmpl), envData)
	}
	return RenderedService{Schema: s, Config: config, Secrets: secrets, Env: env}, nil
}

func (s ServiceSchema) Validate() error {
	if s.Type == "" {
		return fmt.Errorf("service type is required")
	}
	seen := map[string]bool{}
	for _, key := range s.ConfigKeys {
		if key.Key == "" {
			return fmt.Errorf("config key is required for %s", s.Type)
		}
		if seen[key.Key] {
			return fmt.Errorf("duplicate config key %q for %s", key.Key, s.Type)
		}
		seen[key.Key] = true
		if key.Pattern != "" {
			if _, err := regexp.Compile(key.Pattern); err != nil {
				return fmt.Errorf("compile pattern for %s.%s: %w", s.Type, key.Key, err)
			}
		}
	}
	seen = map[string]bool{}
	for _, key := range s.SecretKeys {
		if key.Key == "" {
			return fmt.Errorf("secret key is required for %s", s.Type)
		}
		if seen[key.Key] {
			return fmt.Errorf("duplicate secret key %q for %s", key.Key, s.Type)
		}
		seen[key.Key] = true
		if key.AutoGenerate && key.Length <= 0 {
			return fmt.Errorf("secret key %q length must be positive", key.Key)
		}
	}
	return nil
}

func validateConfigValue(spec ConfigKey, value string) error {
	if spec.Key == "port" && value != "" {
		return validatePort(value)
	}
	if len(spec.Enum) > 0 {
		for _, allowed := range spec.Enum {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("config key %q must be one of %s", spec.Key, strings.Join(spec.Enum, ", "))
	}
	if spec.Pattern == "" || value == "" {
		return nil
	}
	if ok, err := regexp.MatchString(spec.Pattern, value); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("config key %q does not match %s", spec.Key, spec.Pattern)
	}
	return nil
}

func envData(config, secrets map[string]string) map[string]string {
	data := map[string]string{}
	for key, value := range config {
		data[toTemplateKey(key)] = value
	}
	for key, value := range secrets {
		data[toTemplateKey(key)] = value
	}
	return data
}

func toTemplateKey(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func renderTemplate(tmpl string, data map[string]string) string {
	for key, value := range data {
		tmpl = strings.ReplaceAll(tmpl, "{{."+key+"}}", value)
	}
	return tmpl
}

func randomString(reader io.Reader, length int) (string, error) {
	out := make([]byte, length)
	max := big.NewInt(int64(len(secretAlphabet)))
	for i := range out {
		n, err := rand.Int(reader, max)
		if err != nil {
			return "", err
		}
		out[i] = secretAlphabet[n.Int64()]
	}
	return string(out), nil
}

func validatePort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("config key %q must be a TCP port", "port")
	}
	return nil
}

func SortedEnvKeys(values map[string]EnvTemplate) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func EnvKeysForType(serviceType string) ([]string, bool) {
	schema, ok := BuiltInCatalog().Get(serviceType)
	if !ok {
		return nil, false
	}
	return SortedEnvKeys(schema.EnvMapping), true
}
