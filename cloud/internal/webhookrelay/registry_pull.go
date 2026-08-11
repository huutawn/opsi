package webhookrelay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const (
	ghcrPullProviderID    = "ghcr"
	ghcrPullCredentialID  = "hosted-opsi"
	maxRegistrySecretFile = 16 * 1024
)

var ErrRegistryPullCredentialUnavailable = errors.New("registry pull credential unavailable")

type RegistryPullCredentialVault interface {
	Put(context.Context, string, deploymentv1.RegistryPullCredential) error
	Get(context.Context, string) (deploymentv1.RegistryPullCredential, bool, error)
}

type RegistryPullCredentialProvider interface {
	Reference(deploymentv1.ImmutableImage) (*deploymentv1.RegistryPullCredentialReference, bool)
	Resolve(context.Context, deploymentv1.RegistryPullCredentialReference) (deploymentv1.RegistryPullCredential, error)
}

type GHCRRegistryPullCredentialProvider struct {
	prefix string
	vault  RegistryPullCredentialVault
	source RegistryPullConfig
}

func NewGHCRRegistryPullCredentialProvider(config buildjob.RegistryConfig, vault RegistryPullCredentialVault, source RegistryPullConfig) *GHCRRegistryPullCredentialProvider {
	return &GHCRRegistryPullCredentialProvider{prefix: strings.TrimSuffix(config.Host+"/"+config.Namespace+"/"+config.RepositoryPrefix, "/"), vault: vault, source: source}
}

func (p *GHCRRegistryPullCredentialProvider) Reference(image deploymentv1.ImmutableImage) (*deploymentv1.RegistryPullCredentialReference, bool) {
	if p == nil || p.prefix == "" || !image.WithinPrefix(p.prefix) {
		return nil, false
	}
	return &deploymentv1.RegistryPullCredentialReference{Provider: ghcrPullProviderID, CredentialID: ghcrPullCredentialID, Registry: "ghcr.io"}, true
}

func (p *GHCRRegistryPullCredentialProvider) Resolve(ctx context.Context, ref deploymentv1.RegistryPullCredentialReference) (deploymentv1.RegistryPullCredential, error) {
	if p == nil || ref.Provider != ghcrPullProviderID || ref.CredentialID != ghcrPullCredentialID || ref.Registry != "ghcr.io" {
		return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
	}
	if p.source.UsernameFile != "" && p.source.TokenFile != "" {
		username, err := readRegistrySecretFile(p.source.UsernameFile)
		if err != nil {
			return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
		}
		password, err := readRegistrySecretFile(p.source.TokenFile)
		if err != nil {
			return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
		}
		credential := deploymentv1.RegistryPullCredential{Reference: ref, Username: username, Password: password}
		if err := credential.Validate(); err != nil {
			return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
		}
		if p.vault == nil {
			return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
		}
		if err := p.vault.Put(ctx, ref.CredentialID, credential); err != nil {
			return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
		}
		return credential, nil
	}
	if p.vault == nil {
		return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
	}
	credential, ok, err := p.vault.Get(ctx, ref.CredentialID)
	if err != nil || !ok || credential.Validate() != nil || credential.Reference != ref {
		return deploymentv1.RegistryPullCredential{}, ErrRegistryPullCredentialUnavailable
	}
	return credential, nil
}

func (s *Server) associateRegistryPullCredential(image deploymentv1.ImmutableImage, workload *deploymentv1.WorkloadSpec) {
	if workload == nil || s.RegistryPullCredentials == nil {
		return
	}
	if ref, ok := s.RegistryPullCredentials.Reference(image); ok {
		workload.RegistryPullCredential = ref
	}
}

func (s *Server) resolveRegistryPullCredential(ctx context.Context, command *deploymentv1.AgentCommand) {
	if command == nil || command.Workload.RegistryPullCredential == nil || s.RegistryPullCredentials == nil {
		return
	}
	credential, err := s.RegistryPullCredentials.Resolve(ctx, *command.Workload.RegistryPullCredential)
	if err == nil {
		command.RegistryPullCredential = &credential
	}
}

func readRegistrySecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > maxRegistrySecretFile {
		return "", fmt.Errorf("registry credential file has invalid size")
	}
	return strings.TrimSpace(string(data)), nil
}
