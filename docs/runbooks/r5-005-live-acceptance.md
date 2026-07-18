# R5-005 Live GitHub App Acceptance

This runbook covers only the R5-005 GitHub App browser, installation,
repository, binding, Local UI, and `opsi init` acceptance path. It does not add
OIDC, CD, BuildRecord, Agent deployment, MCP, or AI behavior.

## GitHub App Event Policy

The GitHub App `/app` API `events` array represents events selected manually in
the App settings. R5-005 requires the manually subscribed `repository` event.

GitHub sends these lifecycle events to every GitHub App by default and they are
not manually selectable subscriptions:

- `installation`
- `installation_repositories`

Do not require either lifecycle event to appear in the `/app` `events` array.
Do not treat `installation_target` as a replacement for either lifecycle event
or for the required `repository` event. Prove the default lifecycle delivery by
changing the installation's selected-repository access and observing the signed
live webhook.

The App permission boundary remains exactly Metadata: Read-only. Do not add
Contents or any other repository/organization/account permission.

## Sanitized App Preflight

Run the policy tests from a clean final source revision:

```bash
make verify-r5-005-github-app-preflight
```

Run the live verifier only on the staging VPS, where the protected App key is
already mounted. It keeps the App JWT and installation token in process memory,
does not print either credential, rejects redirects and oversized responses,
and prints only sanitized numeric identity/configuration evidence:

```bash
python3 scripts/verify_r5_005_github_app_preflight.py live \
  --app-id 4315525 \
  --private-key /home/ubuntu/opsi/deploy/staging-control-plane/secrets/github-app-private-key.pem \
  --installation-id 147333403 \
  --owner-id 143307746 \
  --repository-id 1304594095 \
  --repository-full-name huutawn/opsi-r5-005-fixture \
  --default-branch main \
  --webhook-url https://opsidev.site/v1/webhooks/github-app
```

Expected sanitized results:

- App ID matches and manual events contain `repository`.
- Default lifecycle events are documented as `installation` and
  `installation_repositories`, not inferred from the App `events` array.
- Permissions are exactly `{"metadata":"read"}`.
- Webhook URL uses public HTTPS, JSON content, and TLS verification.
- Installation `147333403` belongs to account `143307746`, is not suspended,
  and uses selected repositories.
- Installation token creation succeeds without printing the token.
- Repository `1304594095` resolves to the expected full name, owner, and `main`
  default branch.

## Live Lifecycle Evidence

After browser authorization and installation claim have passed:

1. In GitHub, configure installation `147333403` and temporarily remove only
   `huutawn/opsi-r5-005-fixture` from selected repositories.
2. Confirm Cloud receives an `installation_repositories` delivery with action
   `removed` and marks repository ID `1304594095` unavailable/removed.
3. Add the same repository back to selected repositories.
4. Confirm Cloud receives action `added` and restores the same numeric ID.
5. Use GitHub's delivery page to redeliver one completed delivery. Confirm the
   duplicate is recognized without a second state mutation.
6. Restart only Cloud, redeliver again, and confirm PostgreSQL durable delivery
   dedupe still recognizes the delivery.

Never store the raw webhook payload in evidence. Record only delivery ID,
event/action, HTTP result, duplicate flag, numeric installation/repository IDs,
and the resulting sanitized inventory state.
