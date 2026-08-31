package resource

import (
	"context"
	"sort"
	"strconv"
	"strings"

	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
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

		for _, dep := range config.Dependencies {
			if dep.InjectionPhase != "runtime" || dep.TargetKind != "managed_resource" {
				continue
			}
			selectedID := selectedBindings[dep.LogicalName]
			b, ok := resourcecompiler.SelectBinding(bindings, resourcecompiler.BindingIdentity{
				SourceServiceID: applicationID, TargetResourceID: dep.TargetIdentity, LogicalName: dep.LogicalName,
				Protocol: dep.Protocol, Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: selectedID,
			})
			if selectedID == "" || !ok {
				if dep.Required {
					return nil, nil, invalid(resourcev1.FailureBindingSecretMaterialization, "required resource binding authority is unavailable")
				}
				continue
			}
			target, getErr := s.Get(ctx, projectID, dep.TargetIdentity)
			if getErr != nil {
				if dep.Required {
					return nil, nil, invalid(resourcev1.FailureBindingSecretMaterialization, "required resource target is unavailable")
				}
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

			facts := resourcecompiler.ConnectionFacts{Host: host, Port: port, Database: db}
			for _, mapping := range dep.InjectionMappings {
				descriptor, lookupErr := resourcecompiler.LookupSource(dep.Protocol, mapping.SymbolicSource, mapping.Template)
				if lookupErr != nil {
					return nil, nil, invalidCause(resourcev1.FailureBindingSecretMaterialization, "connection mapping is invalid", lookupErr)
				}
				if descriptor.Sensitivity == resourcev1.ValueSecret {
					if credID == "" {
						return nil, nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential is unavailable")
					}
					secrets = append(secrets, deploymentv1.SecretReference{EnvName: mapping.EnvName, SecretID: credID})
					continue
				}
				compiled, compileErr := resourcecompiler.CompileConnection(dep.Protocol, mapping.SymbolicSource, mapping.Template, facts)
				if compileErr != nil {
					return nil, nil, invalidCause(resourcev1.FailureBindingSecretMaterialization, "connection mapping could not be compiled", compileErr)
				}
				environment = append(environment, deploymentv1.EnvironmentVariable{Name: mapping.EnvName, Value: compiled.Value})
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
			selectedID := ""
			if selectedBindings != nil {
				selectedID = selectedBindings[binding.LogicalName]
			}
			selected, ok := resourcecompiler.SelectBinding(bindings, resourcecompiler.BindingIdentity{
				SourceServiceID: applicationID, TargetResourceID: binding.Target.ID, LogicalName: binding.LogicalName,
				Protocol: string(binding.Protocol), Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: selectedID,
			})
			if !ok || selected.ID != binding.ID {
				continue
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
	resourcesByID := make(map[string]resourcev1.Resource, len(resources))
	for _, value := range resources {
		resourcesByID[value.ID] = value
	}
	bindings, err := s.ListBindings(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	reader, hasConfiguration := s.Scopes.(interface {
		GetServiceConfiguration(projectID, serviceID string) (serviceconfigurationv1.Configuration, error)
	})
	if serviceID == "" {
		serviceID = inferSecretMaterialService(references, bindings, resourcesByID)
	}
	grouped := map[string]map[string]string{}
	for _, reference := range references {
		credential, credentialErr := s.Credentials.Get(ctx, reference.SecretID)
		if credentialErr != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential is unavailable")
		}
		value := ""
		if serviceID != "" && credential.ValidateWorkloadSecret(projectID, serviceID) == nil {
			value = credential.Password
		} else {
			if serviceID == "" {
				return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding authority is unavailable")
			}
			config := serviceconfigurationv1.Configuration{}
			if hasConfiguration {
				var configErr error
				config, configErr = reader.GetServiceConfiguration(projectID, serviceID)
				if configErr != nil {
					return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding authority is unavailable")
				}
			}
			mapping, binding, target, mappingErr := selectSecretMapping(config, serviceID, reference, bindings, resourcesByID)
			if mappingErr != nil && len(config.Dependencies) == 0 {
				mapping, binding, target, mappingErr = selectLegacySecretMapping(config, serviceID, reference, bindings, resourcesByID)
			}
			if mappingErr != nil {
				return nil, mappingErr
			}
			if target.Type == resourcev1.TypePostgres {
				if credential.ValidateBinding(binding.ID, target.ID) != nil {
					return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "PostgreSQL binding credential authority is invalid")
				}
			} else if target.Runtime == nil || credential.ValidateFor(target.Type) != nil || credential.Purpose != resourcev1.CredentialPurposeResourceManagement || credential.OwnerID != target.ID || credential.ResourceID != target.ID {
				return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "managed resource credential authority is invalid")
			}
			facts := connectionFacts(target, &binding)
			facts.Username, facts.Password, facts.CredentialAvailable = credential.Username, credential.Password, true
			compiled, compileErr := resourcecompiler.CompileConnection(string(binding.Protocol), mapping.SymbolicSource, mapping.Template, facts)
			if compileErr != nil {
				return nil, invalidCause(resourcev1.FailureBindingSecretMaterialization, "connection mapping could not be compiled", compileErr)
			}
			if compiled.Sensitivity != resourcev1.ValueSecret {
				return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "non-secret resource mapping cannot be materialized as a Secret")
			}
			value = compiled.Value
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

func inferSecretMaterialService(references []deploymentv1.SecretReference, bindings []resourcev1.Binding, resources map[string]resourcev1.Resource) string {
	candidates := map[string]struct{}{}
	for _, reference := range references {
		for _, binding := range bindings {
			if binding.Lifecycle != resourcev1.LifecycleReady {
				continue
			}
			target := resources[binding.Target.ID]
			targetCredential := ""
			if target.Runtime != nil {
				targetCredential = target.Runtime.Spec.CredentialID
			}
			if reference.SecretID == binding.CredentialID || reference.SecretID == targetCredential {
				candidates[binding.Source.ID] = struct{}{}
			}
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	for candidate := range candidates {
		return candidate
	}
	return ""
}

func selectSecretMapping(config serviceconfigurationv1.Configuration, serviceID string, reference deploymentv1.SecretReference, bindings []resourcev1.Binding, resources map[string]resourcev1.Resource) (serviceconfigurationv1.DependencyInjectionMapping, resourcev1.Binding, resourcev1.Resource, error) {
	selected := make(map[string]string, len(config.ResourceBindings))
	for _, value := range config.ResourceBindings {
		selected[value.LogicalName] = value.BindingID
	}
	type candidate struct {
		mapping serviceconfigurationv1.DependencyInjectionMapping
		binding resourcev1.Binding
		target  resourcev1.Resource
	}
	var matches []candidate
	for _, dep := range config.Dependencies {
		if dep.TargetKind != serviceconfigurationv1.TargetKindManagedResource || dep.InjectionPhase != serviceconfigurationv1.InjectionPhaseRuntime {
			continue
		}
		selectedID := selected[dep.LogicalName]
		if selectedID == "" {
			continue
		}
		binding, ok := resourcecompiler.SelectBinding(bindings, resourcecompiler.BindingIdentity{
			SourceServiceID: serviceID, TargetResourceID: dep.TargetIdentity, LogicalName: dep.LogicalName,
			Protocol: dep.Protocol, Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: selectedID,
		})
		if !ok {
			continue
		}
		target, ok := resources[dep.TargetIdentity]
		if !ok {
			continue
		}
		credentialID := binding.CredentialID
		if credentialID == "" && target.Runtime != nil {
			credentialID = target.Runtime.Spec.CredentialID
		}
		if credentialID != reference.SecretID {
			continue
		}
		for _, mapping := range dep.InjectionMappings {
			if mapping.EnvName != reference.EnvName {
				continue
			}
			descriptor, lookupErr := resourcecompiler.LookupSource(dep.Protocol, mapping.SymbolicSource, mapping.Template)
			if lookupErr != nil {
				return serviceconfigurationv1.DependencyInjectionMapping{}, resourcev1.Binding{}, resourcev1.Resource{}, invalidCause(resourcev1.FailureBindingSecretMaterialization, "connection mapping is invalid", lookupErr)
			}
			if descriptor.Sensitivity == resourcev1.ValueSecret {
				matches = append(matches, candidate{mapping: mapping, binding: binding, target: target})
			}
		}
	}
	if len(matches) != 1 {
		return serviceconfigurationv1.DependencyInjectionMapping{}, resourcev1.Binding{}, resourcev1.Resource{}, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret mapping authority is unavailable")
	}
	return matches[0].mapping, matches[0].binding, matches[0].target, nil
}

func selectLegacySecretMapping(config serviceconfigurationv1.Configuration, serviceID string, reference deploymentv1.SecretReference, bindings []resourcev1.Binding, resources map[string]resourcev1.Resource) (serviceconfigurationv1.DependencyInjectionMapping, resourcev1.Binding, resourcev1.Resource, error) {
	selected := make(map[string]string, len(config.ResourceBindings))
	for _, value := range config.ResourceBindings {
		selected[value.LogicalName] = value.BindingID
	}
	type candidate struct {
		mapping serviceconfigurationv1.DependencyInjectionMapping
		binding resourcev1.Binding
		target  resourcev1.Resource
	}
	var matches []candidate
	for _, binding := range bindings {
		selectedBinding, ok := resourcecompiler.SelectBinding(bindings, resourcecompiler.BindingIdentity{
			SourceServiceID: serviceID, TargetResourceID: binding.Target.ID, LogicalName: binding.LogicalName,
			Protocol: string(binding.Protocol), Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: selected[binding.LogicalName],
		})
		if !ok || selectedBinding.ID != binding.ID {
			continue
		}
		target, ok := resources[binding.Target.ID]
		if !ok {
			continue
		}
		credentialID := binding.CredentialID
		if credentialID == "" && target.Runtime != nil {
			credentialID = target.Runtime.Spec.CredentialID
		}
		if credentialID != reference.SecretID {
			continue
		}
		prefix := environmentPrefix(binding.LogicalName) + "_"
		if !strings.HasPrefix(reference.EnvName, prefix) {
			continue
		}
		source := ""
		switch strings.TrimPrefix(reference.EnvName, prefix) {
		case "USER":
			source = serviceconfigurationv1.SourceCredentialUsername
		case "PASSWORD":
			source = serviceconfigurationv1.SourceCredentialPassword
		case "URL":
			source = serviceconfigurationv1.SourceConnectionURL
		}
		if source != "" {
			matches = append(matches, candidate{mapping: serviceconfigurationv1.DependencyInjectionMapping{EnvName: reference.EnvName, SymbolicSource: source}, binding: binding, target: target})
		}
	}
	if len(matches) != 1 {
		return serviceconfigurationv1.DependencyInjectionMapping{}, resourcev1.Binding{}, resourcev1.Resource{}, invalid(resourcev1.FailureBindingSecretMaterialization, "legacy resource binding secret mapping authority is unavailable")
	}
	return matches[0].mapping, matches[0].binding, matches[0].target, nil
}

func connectionFacts(target resourcev1.Resource, binding *resourcev1.Binding) resourcecompiler.ConnectionFacts {
	facts := resourcecompiler.ConnectionFacts{}
	if target.Runtime != nil {
		facts.Host = target.Runtime.Spec.Connection.Host
		facts.Port = strconv.Itoa(int(target.Runtime.Spec.Connection.Port))
		facts.Database = target.Runtime.Spec.Connection.Database
	}
	if binding != nil {
		if value := bindingValue(binding.RuntimeRefs, "HOST"); value != "" {
			facts.Host = value
		}
		if value := bindingValue(binding.RuntimeRefs, "PORT"); value != "" {
			facts.Port = value
		}
		if value := bindingValue(binding.RuntimeRefs, "NAME"); value != "" {
			facts.Database = value
		}
		if binding.Database != "" {
			facts.Database = binding.Database
		}
	}
	return facts
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
