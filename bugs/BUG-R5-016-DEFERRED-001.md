# BUG-R5-016-DEFERRED-001

## UI dependency audit

`npm ci` reports four existing high-severity dependency advisories in the Local
UI dependency tree. R5-016 does not modify `cli/ui/**`, `package.json`, or
`package-lock.json`, so dependency remediation is deferred. UI tests, build,
and lint still pass; this does not alter ActionPlane source correctness.

## Native keychain acceptance

Linux Secret Service behavior is covered with an injected fake `secret-tool`
runner, and macOS Keychain source cross-builds without putting private material
in argv. A real unlocked macOS Keychain was not available, so live native
Keychain acceptance remains deferred with the R5-017 manual environment work.
