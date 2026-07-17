package commands

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

const maxProtectedSecretBytes = 1 << 20

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

func readProtectedSecret(path, label string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s file is required; secret values are not accepted in argv", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s file: %w", label, err)
	}
	defer file.Close()
	if path != "/dev/stdin" {
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect %s file: %w", label, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s file must be a regular file", label)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s file must not be group or world accessible", label)
		}
	}
	value, err := io.ReadAll(io.LimitReader(file, maxProtectedSecretBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file", label)
	}
	if len(value) > maxProtectedSecretBytes {
		clearBytes(value)
		return nil, fmt.Errorf("%s file exceeds 1 MiB", label)
	}
	if len(value) == 0 {
		return nil, errors.New("protected secret file is empty")
	}
	return value, nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
