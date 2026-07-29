# BUG-R5-016-RECOVERY-OPERABILITY-003

Status: FIXED
Baseline revision: e887dc7

Baseline defects:

1. Recovery treated every `CurrentState` error as unavailable, retaining locks
   for ownership and other permanent factual failures.
2. One blocked record could consume the whole recovery pass and starve later
   records.
3. `RecoverLoop` discarded recovery errors with `_ = s.Recover(passCtx)`.

Source fix: unavailable factual state, cancellation, and deadline preserve the
executing record and lock; other factual errors terminalize through the guarded
completion path. Each record receives a bounded share of the pass budget, and
recovery reports sanitized bounded categories while continuing later passes.
