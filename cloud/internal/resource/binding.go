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
	var config serviceconfigurationv1.Configuration
	if reader, ok := s.Scopes.(interface {
		GetServiceConfiguration(projectID, serviceID string) (serviceconfigurationv1.Configuration, error)
	}); ok {
		config, _ = reader.GetServiceConfiguration(projectID, applicationID)
	}

	environment := []deploymentv1.EnvironmentVariable{}
	secrets := []deploymentv1.SecretReference{}

	if len(config.Dependencies) > 0 {
		selectedBindings := make(map[string]string, len(config.ResourceBindings))
		for _, rb := range config.ResourceBindings {
			selectedBindings[rb.LogicalName] = rb.BindingID
		}

		bindingsByID := make(map[string]resourcev1.Binding, len(bindings))
		bindingsByLogical := make(map[string]resourcev1.Binding, len(bindings))
		for _, b := range bindings {
			if b.Source.ID == applicationID {
				bindingsByID[b.ID] = b
				bindingsByLogical[b.LogicalName] = b
			}
		}

		for _, dep := range config.Dependencies {
			if dep.InjectionPhase != "runtime" || dep.TargetKind != "managed_resource" {
				continue
			}
			var b resourcev1.Binding
			var ok bool
			if targetBindingID, hasSelected := selectedBindings[dep.LogicalName]; hasSelected {
				b, ok = bindingsByID[targetBindingID]
			} else {
				b, ok = bindingsByLogical[dep.LogicalName]
				if !ok || b.Target.ID != dep.TargetIdentity {
					for _, candidate := range bindings {
						if candidate.Source.ID == applicationID && candidate.Target.ID == dep.TargetIdentity && candidate.LogicalName == dep.LogicalName {
							b = candidate
							ok = true
							break
						}
					}
				}
			}
			if !ok || b.Lifecycle == resourcev1.LifecycleDeleting {
				continue
			}
			target, getErr := s.Get(ctx, projectID, dep.TargetIdentity)
			if getErr != nil {
				continue
			}

			host := ""
			port := ""
			db := b.Database
			if target.Runtime != nil {
				host = target.Runtime.Spec.Connection.Host
				port = strconv.Itoa(int(target.Runtime.Spec.Connection.Port))
				if db == "" {
					db = target.Runtime.Spec.Connection.Database
				}
			}
			if host == "" {
				host = bindingValue(b.RuntimeRefs, "HOST")
			}
			if port == "" {
				port = bindingValue(b.RuntimeRefs, "PORT")
			}
			if db == "" {
				db = bindingValue(b.RuntimeRefs, "NAME")
			}

			credID := b.CredentialID
			if credID == "" && target.Runtime != nil {
				credID = target.Runtime.Spec.CredentialID
			}

			for _, mapping := range dep.InjectionMappings {
				switch mapping.SymbolicSource {
				case "resource.host":
					if host != "" {
						environment = append(environment, deploymentv1.EnvironmentVariable{Name: mapping.EnvName, Value: host})
					}
				case "resource.port":
					if port != "" {
						environment = append(environment, deploymentv1.EnvironmentVariable{Name: mapping.EnvName, Value: port})
					}
				case "credential.database":
					if db != "" {
						environment = append(environment, deploymentv1.EnvironmentVariable{Name: mapping.EnvName, Value: db})
					}
				case "credential.username":
					if credID != "" {
						secrets = append(secrets, deploymentv1.SecretReference{EnvName: mapping.EnvName, SecretID: credID})
					}
				case "credential.password":
					if credID != "" {
						secrets = append(secrets, deploymentv1.SecretReference{EnvName: mapping.EnvName, SecretID: credID})
					}
				case "connection.url":
					if credID != "" {
						secrets = append(secrets, deploymentv1.SecretReference{EnvName: mapping.EnvName, SecretID: credID})
					}
				}
			}
		}
	} else {
		var selectedBindings map[string]string
		if len(config.ResourceBindings) > 0 {
			selectedBindings = make(map[string]string, len(config.ResourceBindings))
			for _, rb := range config.ResourceBindings {
				selectedBindings[rb.LogicalName] = rb.BindingID
			}
		}
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
	}
	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].EnvName < secrets[j].EnvName })
	return environment, secrets, nil
}

func (s Service) ResolveSecretMaterials(ctx context.Context, projectID, serviceID string, references []deploymentv1.SecretReference) ([]deploymentv1.SecretMaterial, error) {
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
		credential, err := s.Credentials.Get(ctx, reference.SecretID)
		if err != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential is unavailable")
		}
		workloadSecret := false
		if !redisManagement && !postgresBinding {
			workloadSecret = credential.ValidateWorkloadSecret(projectID, serviceID) == nil
			if !workloadSecret {
				return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "workload secret reference is unavailable")
			}
		}
		if postgresBinding && credential.ValidateBinding(authority.binding.ID, authority.target.ID) != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "PostgreSQL binding credential authority is invalid")
		}

		var mappedSource string
		if reader, ok := s.Scopes.(interface {
			GetServiceConfiguration(projectID, serviceID string) (serviceconfigurationv1.Configuration, error)
		}); ok && authority.binding != nil {
			if cfg, err := reader.GetServiceConfiguration(projectID, authority.binding.Source.ID); err == nil {
				for _, dep := range cfg.Dependencies {
					if dep.LogicalName == authority.binding.LogicalName {
						for _, m := range dep.InjectionMappings {
							if m.EnvName == reference.EnvName {
								mappedSource = m.SymbolicSource
								break
							}
						}
					}
				}
			}
		}

		upper := strings.ToUpper(reference.EnvName)
		value := ""
		switch {
		case workloadSecret:
			value = credential.Password
		case mappedSource == "credential.username" || strings.HasSuffix(upper, "_USER") || upper == "USER" || upper == "PGUSER":
			value = credential.Username
		case mappedSource == "credential.password" || strings.HasSuffix(upper, "_PASSWORD") || strings.HasSuffix(upper, "_PASS") || upper == "PASSWORD" || upper == "PGPASSWORD" || upper == "PASS":
			value = credential.Password
		case mappedSource == "connection.url" || strings.HasSuffix(upper, "_URL") || upper == "URL" || strings.Contains(upper, "DATABASE_URL") || strings.Contains(upper, "REDIS_URL"):
			scheme := "redis"
			host, port, database := authority.target.Runtime.Spec.Connection.Host, strconv.Itoa(int(authority.target.Runtime.Spec.Connection.Port)), ""
			if authority.binding != nil {
				scheme, host, port, database = "postgres", bindingValue(authority.binding.RuntimeRefs, "HOST"), bindingValue(authority.binding.RuntimeRefs, "PORT"), bindingValue(authority.binding.RuntimeRefs, "NAME")
				if host == "" && authority.target.Runtime != nil {
					host = authority.target.Runtime.Spec.Connection.Host
					port = strconv.Itoa(int(authority.target.Runtime.Spec.Connection.Port))
				}
				if database == "" {
					database = authority.binding.Database
				}
				if database == "" && authority.target.Runtime != nil {
					database = authority.target.Runtime.Spec.Connection.Database
				}
			}
			var connection url.URL
			if scheme == "redis" {
				connection = url.URL{Scheme: scheme, User: url.UserPassword(credential.Username, credential.Password), Host: host + ":" + port}
			} else {
				connection = url.URL{Scheme: scheme, User: url.UserPassword(credential.Username, credential.Password), Host: host + ":" + port}
				if database != "" {
					connection.Path = database
					connection.RawQuery = "sslmode=disable"
				}
			}
			value = connection.String()
		default:
			if strings.Contains(upper, "USER") {
				value = credential.Username
			} else if strings.Contains(upper, "PASS") || strings.Contains(upper, "PWD") {
				value = credential.Password
			} else if strings.Contains(upper, "URL") {
				scheme := "redis"
				host, port, database := authority.target.Runtime.Spec.Connection.Host, strconv.Itoa(int(authority.target.Runtime.Spec.Connection.Port)), ""
				if authority.binding != nil {
					scheme, host, port, database = "postgres", bindingValue(authority.binding.RuntimeRefs, "HOST"), bindingValue(authority.binding.RuntimeRefs, "PORT"), bindingValue(authority.binding.RuntimeRefs, "NAME")
					if database == "" {
						database = authority.binding.Database
					}
				}
				connection := url.URL{Scheme: scheme, User: url.UserPassword(credential.Username, credential.Password), Host: host + ":" + port}
				if database != "" {
					connection.Path = database
					connection.RawQuery = "sslmode=disable"
				}
				value = connection.String()
			} else {
				return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret name is unsupported")
			}
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
