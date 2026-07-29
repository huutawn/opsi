# BUG-R5-016-CLI-HYGIENE-002

## Factual backlog

1. `device revoke` currently ignores failure to delete the private key from the
   OS secure store.
2. Human approval currently displays `TargetIdentity.Key()`, whose component
   separators are NUL bytes.

These issues are not fixed by R5-016B. Neither is a current approval-bypass
path: approval remains bound to the persisted challenge, signed grant, device,
project, user, nonce, plan hash, and factual state hash.

Both issues must be handled in the consolidated manual bug-fix phase before
R5-017 final acceptance.
