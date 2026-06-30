# Opsi CLI UI

Next.js static-export console served by `opsi start` from `cli/ui/out`.

## Commands

```bash
npm run lint
npm run build
```

## Structure

- `app/`: route shell only.
- `components/`: shared layout and primitives.
- `features/`: product workflow views.
- `hooks/`: client state orchestration.
- `lib/api`: typed Cloud registry client.
- `lib/contracts`: UI-facing registry contracts.

PAT is held in React memory only. Browser storage is intentionally unused.
