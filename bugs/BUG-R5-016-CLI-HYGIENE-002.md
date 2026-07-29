# BUG-R5-016-CLI-HYGIENE-002

Status: FIXED

## Factual backlog

1. `device revoke` currently ignores failure to delete the private key from the
   OS secure store.
2. Human approval currently displays `TargetIdentity.Key()`, whose component
   separators are NUL bytes.

Both source issues are fixed. Device revoke always attempts local private-key
cleanup and returns the typed `ACTION_SECURE_CLEANUP_REQUIRED` receipt on
failure. Human approval displays labeled, bounded fields without
`TargetIdentity.Key()` or control characters. Approval remains bound to the
persisted challenge, signed grant, device, project, user, nonce, plan hash, and
factual state hash.

Both issues must be handled in the consolidated manual bug-fix phase before
R5-017 final acceptance.
