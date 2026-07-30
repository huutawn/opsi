# BUG-UI-DELIVERY-GITHUB-RUN-VISIBILITY-001

Status: Open; UI-only delivery gap

## Evidence

- A repository-wide code search found no Local API or frontend contract for GitHub Actions run inventory or failed-run retrieval.
- The available Delivery source is the accepted Cloud `BuildRecord` endpoint (`/api/local/projects/:projectID/build-records`).
- A failed GitHub Actions run that never submits a `BuildRecord` therefore cannot be distinguished from a workflow that has not yet delivered its record.

## Affected UI

Delivery → Pipeline and Delivery → Builds cannot truthfully show a pre-BuildRecord GitHub Actions failure.

## Truthful fallback

The UI renders `No trusted BuildRecord received` / `Build records unavailable` and explicitly avoids calling this state `Build failed`.

## Required follow-up scope

Add a Local-mediated, exact repository/run identity endpoint and frontend contract for GitHub Actions run visibility, including failed runs, then correlate it by repository ID, workflow/run ID, attempt, ref, and SHA. No backend change is made in FE-02.
