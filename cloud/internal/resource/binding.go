package resource

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func (s Service) ApplicationEnvironment(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, error) {
	environment, _, err := s.ApplicationRuntimeConfiguration(ctx, projectID, environmentID, applicationID)
	return environment, err
}

func (s Service) ApplicationRuntimeConfiguration(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, []deploymentv1.SecretReference, error) {
	bindings, err := s.ListBindings(ctx, projectID, environmentID)
	if err != nil {
		return nil, nil, err
	}
	var selectedBindings map[string]string
	if reader, ok := s.Scopes.(interface {
		GetServiceConfiguration(projectID, serviceID string) (serviceconfigurationv1.Configuration, error)
	}); ok {
		if config, err := reader.GetServiceConfiguration(projectID, applicationID); err == nil && len(config.ResourceBindings) > 0 {
			selectedBindings = make(map[string]string, len(config.ResourceBindings))
			for _, rb := range config.ResourceBindings {
				selectedBindings[rb.LogicalName] = rb.BindingID
			}
		}
	}
	environment := []deploymentv1.EnvironmentVariable{}
	secrets := []deploymentv1.SecretReference{}
	for _, binding := range bindings {
		if binding.Source.ID != applicationID || binding.Lifecycle != resourcev1.LifecycleReady {
			continue
		}
		if selectedBindings != nil {
			if targetID, ok := selectedBindings[binding.LogicalName]; ok && binding.ID != targetID {
				continue
			}
		}
		prefix := environmentPrefix(binding.LogicalName)
		for _, reference := range binding.RuntimeRefs {
			name := prefix + "_" + reference.Name
			if reference.Sensitivity == resourcev1.ValueSecret && reference.SecretRef != nil {
				secrets = append(secrets, deploymentv1.SecretReference{EnvName: name, SecretID: reference.SecretRef.SecretID})
			} else if reference.Sensitivity == resourcev1.ValueNonSecret && reference.Value != "" {
				environment = append(environment, deploymentv1.EnvironmentVariable{Name: name, Value: reference.Value})
			}
		}
	}
	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].EnvName < secrets[j].EnvName })
	return environment, secrets, nil
}

func (s Service) ResolveSecretMaterials(ctx context.Context, projectID string, references []deploymentv1.SecretReference) ([]deploymentv1.SecretMaterial, error) {
	if len(references) == 0 {
		return nil, nil
	}
	if s.Credentials == nil {
		return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential authority is unavailable")
	}
	resources, err := s.List(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	type secretAuthority struct {
		target  resourcev1.Resource
		binding *resourcev1.Binding
	}
	byCredential := map[string]secretAuthority{}
	for _, value := range resources {
		if value.Runtime != nil && value.Runtime.Spec.CredentialID != "" {
			byCredential[value.Runtime.Spec.CredentialID] = secretAuthority{target: value}
		}
	}
	bindings, err := s.ListBindings(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	for index := range bindings {
		binding := &bindings[index]
		if binding.Lifecycle == resourcev1.LifecycleReady && binding.CredentialID != "" {
			target, getErr := s.Get(ctx, projectID, binding.Target.ID)
			if getErr != nil {
				return nil, getErr
			}
			byCredential[binding.CredentialID] = secretAuthority{target: target, binding: binding}
		}
	}
	grouped := map[string]map[string]string{}
	for _, reference := range references {
		authority, ok := byCredential[reference.SecretID]
		redisManagement := ok && authority.binding == nil && authority.target.Type == resourcev1.TypeRedis && authority.target.Runtime != nil && authority.target.Runtime.Spec.CredentialID == reference.SecretID
		postgresBinding := ok && authority.binding != nil && authority.target.Type == resourcev1.TypePostgres
		if !redisManagement && !postgresBinding {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret reference is unavailable")
		}
		credential, err := s.Credentials.Get(ctx, reference.SecretID)
		if err != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential is unavailable")
		}
		if postgresBinding && credential.ValidateBinding(authority.binding.ID, authority.target.ID) != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "PostgreSQL binding credential authority is invalid")
		}
		value := ""
		switch {
		case strings.HasSuffix(reference.EnvName, "_USER"):
			value = credential.Username
		case strings.HasSuffix(reference.EnvName, "_PASSWORD"):
			value = credential.Password
		case strings.HasSuffix(reference.EnvName, "_URL"):
			scheme := "redis"
			host, port, database := authority.target.Runtime.Spec.Connection.Host, strconv.Itoa(int(authority.target.Runtime.Spec.Connection.Port)), ""
			if authority.binding != nil {
				scheme, host, port, database = "postgres", bindingValue(authority.binding.RuntimeRefs, "HOST"), bindingValue(authority.binding.RuntimeRefs, "PORT"), bindingValue(authority.binding.RuntimeRefs, "NAME")
			}
			connection := url.URL{Scheme: scheme, User: url.UserPassword(credential.Username, credential.Password), Host: host + ":" + port}
			if database != "" {
				connection.Path = database
				connection.RawQuery = "sslmode=disable"
			}
			value = connection.String()
		default:
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret name is unsupported")
		}
		if grouped[reference.SecretID] == nil {
			grouped[reference.SecretID] = map[string]string{}
		}
		grouped[reference.SecretID][reference.EnvName] = value
	}
	result := make([]deploymentv1.SecretMaterial, 0, len(grouped))
	for id, values := range grouped {
		result = append(result, deploymentv1.SecretMaterial{SecretID: id, Values: values})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SecretID < result[j].SecretID })
	return result, nil
}

func bindingValue(references []resourcev1.RuntimeConnectionReference, name string) string {
	for _, reference := range references {
		if reference.Name == name {
			return reference.Value
		}
	}
	return ""
}

func environmentPrefix(value string) string {
	var out strings.Builder
	underscore := false
	for _, r := range strings.ToUpper(value) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
			underscore = false
		} else if !underscore {
			out.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(out.String(), "_")
}
