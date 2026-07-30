# BUG-R5-015-UI-NPM-AUDIT-001

## Summary

The UI dependency audit remains upstream-blocked. With the compatible latest
stable `next@16.2.12` and `eslint-config-next@16.2.12`, npm reports four high
findings including three findings in the `--omit=dev` dependency view. The
frontend source redesign does not ship these packages to the browser runtime,
but PostCSS remains build-time reachable and Sharp remains an optional Next
dependency.

## Reproduction

```bash
cd cli/ui
npm ci
npm explain postcss
npm explain sharp
npm audit --json
npm audit --omit=dev --json
```

Observed after FE-04: `npm audit` reports four high findings; the production
view reports three high findings. Exact advisories are
`GHSA-6g55-p6wh-862q` and `GHSA-r28c-9q8g-f849` through
`next@16.2.12 -> postcss@8.4.31`, and `GHSA-f88m-g3jw-g9cj` through
`next@16.2.12 -> sharp@0.34.5`. The fourth full-audit finding is the dev-only
`brace-expansion` path (`GHSA-3jxr-9vmj-r5cp`, `GHSA-mh99-v99m-4gvg`).
`npm audit` suggests the incompatible `next@9.3.3`; no compatible stable fix
exists in the pinned toolchain, and no override, downgrade, prerelease, or
new direct PostCSS/Sharp dependency is allowed.

## Affected files

- `cli/ui/package.json`
- `cli/ui/package-lock.json`

## Reachability and closure

`next.config.ts` remains `output: "export"`. The exported `cli/ui/out`
contains static HTML/JS/CSS/text assets only: no Next server or `node_modules`
directory is distributed, no PostCSS or Sharp package content is present, and
source has no `next/image` usage.
Sharp is not exercised by the current UI path. PostCSS is still reachable at
build time through Next, so this is not risk-free.

## Status

`OPEN / UPSTREAM_BLOCKED / NOT_SHIPPED_TO_BROWSER_RUNTIME / BUILD_TIME_RISK_REMAINS`

Closure requires a compatible stable Next release that removes the affected
PostCSS and Sharp paths without downgrading, prerelease adoption, overrides, or
new direct dependencies. No new advisory was introduced by FE-04.
