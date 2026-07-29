# BUG-R5-016-DEFERRED-001

## UI dependency audit

`npm ci` reports four existing high-severity dependency advisories in the Local
UI dependency tree. R5-016 does not modify `cli/ui/**`, `package.json`, or
`package-lock.json`, so dependency remediation is deferred. UI tests, build,
and lint still pass; this does not alter ActionPlane source correctness.

## Native keychain acceptance

Linux Secret Service is the ActionPlane secure backend covered by source tests
with an injected fake `secret-tool` runner. Private keys and pending grants are
sent through stdin and have no plaintext fallback.

A real unlocked macOS Keychain was not available. Cross-build does not prove
that `security add-generic-password` durably stores stdin, so R5-016 makes no
macOS ActionPlane secure-storage claim. Darwin private-key and pending-grant
operations now fail closed with `ErrActionStoreUnverified`; PAT operations keep
their existing behavior. Native macOS acceptance must verify store/get/delete,
locked-keychain errors, timeout behavior, restart persistence, cleanup retry,
and absence of secrets in argv/output before this backend gap can be closed.

## CLI hygiene backlog

R5-016B does not modify CLI behavior. The secure-store deletion error and NUL
separator display issues are tracked factually in
`bugs/BUG-R5-016-CLI-HYGIENE-002.md` for the consolidated manual bug-fix phase
before R5-017 final acceptance.
