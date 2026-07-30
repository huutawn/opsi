# BUG-UI-DELIVERY-LOADING-STATE-002

## Defect

`DeliverySource` includes a `loading` state, but `PipelineView` only distinguishes `unavailable`. The remaining states, including `loading`, are treated as `ready`.

During the initial request, the pipeline can therefore render `Source not configured` or `No trusted BuildRecord received` before the API responds.

## Scope

This is a factual FE-02 defect record only. Delivery source behavior is not changed in FE-03.

## Follow-up

Fix the source-state mapping in FE-04 stabilization and cover the initial loading transition with a regression test.
