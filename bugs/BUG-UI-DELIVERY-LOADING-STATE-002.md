# BUG-UI-DELIVERY-LOADING-STATE-002

## Defect

`DeliverySource` includes a `loading` state, but `PipelineView` only distinguishes `unavailable`. The remaining states, including `loading`, are treated as `ready`.

During the initial request, the pipeline can therefore render `Source not configured` or `No trusted BuildRecord received` before the API responds.

## Scope

This is a factual FE-02 defect record. Delivery source behavior was not changed in FE-03.

## FE-04 baseline reproduction

The FE-04 regression requires `PipelineView` to recognize `sourceState === "loading"`. It fails at the starting revision because the view maps every non-`unavailable` source state to `ready` and can render empty conclusions before the Local API request settles.

## Status

`FIXED / FE-04`

`DeliveryData.hasLoaded` keeps the loading state distinct, preserves the previous factual result during refresh, and prevents empty conclusions before the Local request settles. The focused source regression and Playwright loading scenario pass.
