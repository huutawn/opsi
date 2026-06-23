package commands

import "github.com/opsi-dev/opsi/cli/internal/keychain"

func optionalPAT(factory func() (keychain.Store, error)) string {
	if factory == nil {
		return ""
	}
	store, err := factory()
	if err != nil {
		return ""
	}
	pat, err := store.GetPAT()
	if err != nil {
		return ""
	}
	return pat
}
