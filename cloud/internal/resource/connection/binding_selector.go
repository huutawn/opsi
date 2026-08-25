package connection

import resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"

type BindingIdentity struct {
	SourceServiceID   string
	TargetResourceID  string
	LogicalName       string
	Protocol          string
	Lifecycle         resourcev1.LifecycleState
	SelectedBindingID string
}

// SelectBinding is the sole selector for a dependency's resource binding.
// An identity without a selected ID is accepted only when exactly one binding
// matches every other authority field.
func SelectBinding(bindings []resourcev1.Binding, identity BindingIdentity) (resourcev1.Binding, bool) {
	var selected resourcev1.Binding
	found := false
	for _, binding := range bindings {
		if binding.Source.Kind != resourcev1.KindApplication || binding.Source.ID != identity.SourceServiceID ||
			binding.Target.Kind != resourcev1.KindManagedService || binding.Target.ID != identity.TargetResourceID ||
			binding.LogicalName != identity.LogicalName || string(binding.Protocol) != identity.Protocol ||
			binding.Lifecycle != identity.Lifecycle ||
			(identity.SelectedBindingID != "" && binding.ID != identity.SelectedBindingID) {
			continue
		}
		if found {
			return resourcev1.Binding{}, false
		}
		selected, found = binding, true
	}
	return selected, found
}
