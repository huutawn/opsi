# Credential Incident Runbook

Status: operator action required. Packaging hardening prevents known runtime
credential paths and private-key markers from entering new source artifacts,
but it does not revoke credentials that may already have been disclosed.

## Safety rules

- Treat every credential listed below as potentially exposed until its owner
  records a successful rotation and verification.
- Do not print, paste, diff, attach, or store secret values in tickets, shell
  history, logs, evidence, chat, or source control.
- Record only credential class, owner, rotation time, provider-side identifier,
  verification result, and redacted audit references.
- Stop distribution of affected archives and preserve hashes, timestamps, and
  access metadata. Do not preserve secret-bearing copies in ordinary evidence.
- External revocation and Git history cleanup require explicit owner authority.

## Credential classes

- GitHub App private key
- GitHub App client secret
- GitHub App webhook secret
- PostgreSQL password
- Bootstrap Worker token
- Bootstrap encryption key
- Alert internal token
- SMTP credential
- Initial PAT, if one exists

## Rotation order

1. Contain distribution, identify the exposure window, and confirm which
   environments consumed the affected archive.
2. Rotate the GitHub App private key, deploy the replacement, verify new App
   authentication, and revoke the old key.
3. Rotate the GitHub App client secret and webhook secret, then verify login and
   signed webhook handling while confirming old material is rejected.
4. Rotate the initial PAT if it exists, Bootstrap Worker token, and alert
   internal token. Restart or reload only the services that own those values.
5. Rotate the PostgreSQL password using a staged database-role or coordinated
   credential change that avoids losing all administrative access.
6. Rotate the SMTP credential and verify a bounded test delivery without
   logging message credentials or authorization headers.
7. Rotate the bootstrap encryption key with a reviewed migration plan. Drain or
   cancel affected bootstrap work first, back up encrypted state, re-encrypt or
   invalidate dependent records, and verify that old ciphertext cannot be used
   with the new key.

If dependency analysis shows a more directly exploitable credential, the
incident owner may move it earlier but must record the reason and verification.

## Verification after rotation

- Confirm services start with validated configuration and healthy dependencies.
- Confirm the replacement credential succeeds through its normal bounded path.
- Confirm the previous credential is revoked or rejected; never include the
  rejected value in the test command or evidence.
- Review provider and application audit metadata for unexpected use during and
  after the exposure window.
- Verify GitHub App signing, user authorization, and webhook validation as
  separate checks; success in one does not prove the others.
- Verify PostgreSQL reconnect, migrations, and least-privilege role behavior.
- Verify Bootstrap Worker and alert internal authentication with new material
  and rejection of old material.
- Verify SMTP delivery and initial PAT replacement or revocation, when present.
- Record `PASS`, `FAIL`, or `NOT_APPLICABLE` per credential class with timestamp
  and owner. Do not record a secret value or reversible encoding.

## Rollback considerations

- Prefer configuration rollback to credential rollback. A compromised revoked
  credential must not be restored merely to recover service.
- Where a provider supports overlap, deploy and verify new material before
  revoking old material, then keep the overlap window as short as possible.
- Preserve a tested break-glass administrative path before database credential
  changes, without copying credentials into the incident record.
- Snapshot encrypted bootstrap state before encryption-key migration. Roll back
  application code only if the new key and migrated state remain protected.
- If verification fails, isolate the affected service, diagnose with redacted
  metadata, and issue another replacement rather than reactivating exposed data.

## Git history scan and cleanup

1. Search all refs for affected path names using name-only Git commands.
2. Run an approved secret scanner across all refs with redaction enabled and
   configure reports to contain only rule ID, commit ID, path, and status.
3. Review forks, mirrors, CI artifacts, release attachments, caches, backups,
   and downloaded archives for the same exposure window.
4. Store the redacted scan report in restricted incident evidence and verify
   that the report itself contains no matched value.
5. Decide whether history rewriting is warranted only after credential rotation
   and stakeholder impact review.

Git history cleanup is a separate repository-owner action. It is not performed
by packaging hardening, and it requires coordination for force-pushes, forks,
mirrors, cached clones, release artifacts, and downstream consumers.

## Closure gate

The incident may close only when every applicable credential class has an owner,
rotation or revocation evidence, post-rotation verification, and a recorded Git
history decision. Passing source-package tests alone is not incident closure.
