# BUG-R5-012-AUTO-DEPLOYMENT-JOB-001

Status: SOURCE_FIXED / LIVE_RETEST_PENDING
Milestone: stabilization-before-R5-017
Owner: Cloud CD orchestration
Observed revision: 870f076943e2dfc3d1a1b30751fa9d15683437f6
Fixture main commit: b9df505
BuildRecord: br-1ed0f6d8875cca9993e7ca4e023db3d0
Routing result: ROUTING_ELIGIBLE

Defect: accepted BuildRecords with eligible automatic routing could return a
successful response while DeploymentJob creation failed.

Impact: duplicate, restart, rollback and preview live acceptance remain blocked.

Source fix: the endpoint now returns sanitized `503
AUTOMATIC_DELIVERY_PENDING` after durable acceptance, and the GitHub Actions
helper retries at most three times with a fresh OIDC token per attempt. Existing
canonical BuildRecord/DeploymentJob idempotency remains authoritative.

Documented status: `SOURCE_FIXED / LIVE_RETEST_PENDING`. No live retest was
performed in this task.
