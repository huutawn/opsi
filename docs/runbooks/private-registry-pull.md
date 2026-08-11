# Private registry pull credential

Opsi uses one dedicated Hosted Opsi GHCR identity for runtime pulls. Grant only
`read:packages` for the private package scope; do not reuse a build workflow
token, source token, browser PAT, GitHub App private key, or bootstrap secret.

Configure the Cloud process with file paths, not credential values:

```text
OPSI_CLOUD_GHCR_PULL_USERNAME_FILE=/run/secrets/opsi-ghcr-pull-username
OPSI_CLOUD_GHCR_PULL_TOKEN_FILE=/run/secrets/opsi-ghcr-pull-token
```

Cloud reads these files only when a canonical private GHCR deployment needs the
credential, then stores the value encrypted with the existing
`OPSI_CLOUD_BOOTSTRAP_SECRET_KEY` authority. Fresh Connect Server bootstrap does
not require either file.

The Agent maintains `opsi-registry-ghcr-hosted-opsi` in the exact workload
namespace. Workloads reference it only through `imagePullSecrets`; the secret is
not mounted or exposed as environment, configuration, annotations, or user API.

## Rotation

1. Create a replacement read-only package token for the same Hosted Opsi pull identity.
2. Atomically replace the token file; restart Cloud only when the secret mount does not refresh files in place.
3. Redeploy or restart a private workload. The next lease updates the encrypted Cloud value and replaces the managed Kubernetes secret only when its Docker config changed.
4. Verify the workload reaches `Ready` and its `containerStatuses[].imageID` contains the accepted BuildRecord digest, then revoke the previous token.

Revocation does not change factual state for already-running containers. The
next pull, restart, or redeploy reports registry authentication or image-pull
failure; rollback remains limited to the exact known-good BuildRecord digest.
