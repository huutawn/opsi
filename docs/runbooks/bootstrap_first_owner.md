# Bootstrap The First Owner

This runbook provisions the first Cloud owner and project through the local
`opsi-cloud` binary. It does not use manual SQL and does not expose a network
bootstrap endpoint.

## Prerequisites

- A Cloud JSON config whose `database_url` points to the durable PostgreSQL
  database used by Cloud.
- The Cloud schema account can run the normal idempotent migrations.
- The selected OAuth provider matches `auth.provider` in the Cloud config, or a
  secure absolute path is available for a one-time initial PAT file.
- Run the command on a trusted control-plane host with appropriate OS access to
  the Cloud config and PAT destination directory.

## OAuth mode

Prelink the provider's stable verified subject. Both OAuth flags are required:

```bash
opsi-cloud admin bootstrap-owner \
  --config /etc/opsi/cloud.json \
  --email owner@example.invalid \
  --display-name "Example Owner" \
  --org-name "Example Organization" \
  --project-name "Example Project" \
  --oauth-provider github \
  --oauth-subject 12345678
```

Normal browser OAuth still verifies the provider callback. It authorizes the
prelinked provider and subject; callback email alone is not sufficient.

## Initial PAT file mode

The command generates the PAT internally. There is no `--pat`, `--token`, or
password argument:

```bash
opsi-cloud admin bootstrap-owner \
  --config /etc/opsi/cloud.json \
  --email owner@example.invalid \
  --org-name "Example Organization" \
  --org-slug example-organization \
  --project-name "Example Project" \
  --project-slug example-project \
  --pat-output-file /secure/path/initial-owner.pat
```

The parent directory must already exist. The destination must be an absolute
path outside a Git worktree. A newly issued PAT is written with mode `0600` and
an existing destination is never overwritten. Move the usable credential into
the supported client keychain, then remove the file according to the host's
secret-handling policy. Do not print or copy its contents into logs or tickets.

OAuth and PAT modes may be requested together.

## Repeat and conflict behavior

An exact repeat returns the same user, organization, and project IDs. It does
not duplicate memberships or OAuth links and never issues a second initial PAT.
Because a stored PAT hash cannot recover the plaintext, a repeat reports that
the existing secret is not recoverable.

A different email, organization slug, project slug, existing project Owner, or
conflicting OAuth subject fails closed with a typed conflict. The command never
changes ownership to a different user. Use the normal membership and PAT
lifecycle for later administration or credential rotation.

## Verify through supported APIs

Use the PAT from the secure file or complete browser OAuth login, then call the
normal project-scoped APIs through the supported CLI/local backend. Confirm that
the project list contains the returned project ID and that authenticated PAT
verification returns role `owner` for that project. The Cloud project response
also exposes the canonical project slug and readiness state.

No database inspection or direct database mutation is part of this procedure.
