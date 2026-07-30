# BUG-R5-016-DEFERRED-001

## UI dependency audit

FE-04 keeps the compatible latest stable `next@16.2.12` and documents the
remaining upstream findings as
`OPEN / UPSTREAM_BLOCKED / NOT_SHIPPED_TO_BROWSER_RUNTIME /
BUILD_TIME_RISK_REMAINS`. Production audit output contains
`GHSA-6g55-p6wh-862q` and `GHSA-r28c-9q8g-f849` through
`next@16.2.12 -> postcss@8.4.31`, plus `GHSA-f88m-g3jw-g9cj` through the
optional `next@16.2.12 -> sharp@0.34.5` path. The full audit also contains the
dev-only brace-expansion advisories `GHSA-3jxr-9vmj-r5cp` and
`GHSA-mh99-v99m-4gvg`. Static export inspection shows no PostCSS or Sharp in
browser artifacts; PostCSS remains build-time reachable. This is not dependency
remediation or supply-chain closure. The macOS Keychain deferred claim below
is unchanged.

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

## Remaining deferred scope

The UI dependency advisories and native macOS ActionPlane secure-storage
acceptance remain deferred to R5-017. CLI hygiene is tracked separately and is
fixed in `bugs/BUG-R5-016-CLI-HYGIENE-002.md`.
