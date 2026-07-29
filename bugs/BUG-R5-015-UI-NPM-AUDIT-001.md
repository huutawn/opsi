# BUG-R5-015-UI-NPM-AUDIT-001

## Summary

The mandatory repository `make verify` gate succeeds, but `npm ci` reports four
high-severity dependency audit findings and pending install-script review for
`sharp@0.34.5` and `unrs-resolver@1.12.2`.

## Reproduction

```bash
cd cli/ui
npm ci
```

Observed output includes `4 high severity vulnerabilities` and two packages
whose install scripts are not covered by the current npm allow-scripts policy.

## Affected files

- `cli/ui/package.json`
- `cli/ui/package-lock.json`

## Severity

High as reported by npm; runtime applicability has not been assessed.

## Deferred phase

R5-017 UI redesign and browser acceptance. R5-015 forbids UI/dependency changes,
and the warning does not block the bounded Agent/CLI/Local API evidence source.
