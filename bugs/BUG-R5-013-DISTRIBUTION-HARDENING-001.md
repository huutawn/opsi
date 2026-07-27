# BUG-R5-013-DISTRIBUTION-HARDENING-001

## R5-014 factual baseline

- `.github/workflows/release-cli.yml` used mutable `actions/checkout@v4` and `actions/setup-go@v5` references.
- R5-013 release packaging built CLI binaries but did not package `cli/ui/out`.
- A standalone installed CLI could therefore return `UI build not found` from `opsi start`.
- `OPSI_RELEASE_BASE_URL` downloaded both the artifact and checksum from the same configurable authority; a custom source was not independently trusted.

R5-014 addresses the packaging and action-pin issues by shipping one archive containing the binary and adjacent `opsi-ui` assets. Custom mirrors require explicit unsafe opt-in and are not described as independently verified.
