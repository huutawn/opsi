# Opsi CLI UI

Next.js static-export console served by `opsi start` from `cli/ui/out`.

## Commands

```bash
npm test
npm run lint
npm run build
```

## Structure

- `app/`: route shell only.
- `components/`: shared layout and primitives.
- `features/`: product workflow views.
- `hooks/`: client state orchestration.
- `lib/api`: typed local backend client.
- `lib/contracts`: UI-facing registry contracts.
- `lib/i18n`: internationalization catalogs (en/vi), dynamic locale resolution, and date/time formatters.

## Storage Policy

Long-lived credentials, certificates, and session tokens stay strictly in the CLI backend and OS keychain; they are never written to browser storage. LocalStorage is restricted exclusively to non-sensitive user preferences (such as the interface locale selection).
