# ADR-007: Canonical Traefik external exposure

Status: Accepted for the R5-011.1 local contract and renderer checkpoint.

## Context

R5-010 owns deterministic namespaces, resource names, labels, the immutable
workload spec hash, and one ClusterIP Service per production workload. R5-011
adds external exposure, but supporting several Kubernetes gateway resources or
accepting controller-specific user configuration would create parallel
execution paths and weaken ownership checks.

K3s provides Traefik support for standard `networking.k8s.io/v1` Ingress. The
standard resource expresses every R5-011.1 invariant: one hostname, Prefix
path semantics, an exact Service/port backend, a fixed ingress class, and an
optional namespace-local TLS Secret.

## Decision

`ExposureSpec v1` is the sole external exposure contract. It carries Opsi
project/environment/runtime/service/deployment identity, one canonical ASCII
DNS hostname, one canonical Prefix path, the exact Service port, a bounded TLS
mode/reference, optional bounded display metadata, and its deterministic hash.
It carries no Kubernetes names, manifests, annotations, middleware, secret
values, credentials, or controller configuration.

The Agent renders exactly one `networking.k8s.io/v1` Ingress with
`ingressClassName: traefik` and field manager `opsi-r5-011-exposure`. Namespace,
Ingress name, labels, backend Service, and Service port are derived from the
authoritative R5-010 renderer and workload identity. The fixed annotations are
limited to the exposure spec hash, workload spec hash, and exact identity hash.
No IngressRoute, Gateway API, Nginx annotation, raw Traefik middleware, or
arbitrary manifest renderer exists.

Hostnames canonicalize to lowercase and must remain ASCII DNS names. Schemes,
ports, paths, IP literals, localhost names, wildcards, Unicode/IDNA input,
empty labels, and invalid DNS bounds fail typed. Paths use Prefix semantics,
retain `/`, remove one trailing slash from non-root paths, and reject query,
fragment, repeated slash, dot segments, backslash, control characters, invalid
UTF-8, and all percent encoding in the MVP. Rejecting percent encoding avoids
multiple equivalent or traversal-prone route representations. Route conflicts
are path-component aware: `/api` overlaps `/api/v1`, not `/api2`, and `/`
overlaps every path on the same hostname.

Read-only preflight uses the existing Agent `CommandRunner`/kubectl boundary.
It distinguishes absent, unchanged, and owned-changed resources; emits a
deterministic hash-only diff; and fails closed for same-name foreign Ingresses,
Opsi or foreign host/path conflicts, cross-workload identity, and missing,
foreign, or mismatched backend Services. Foreign conflict results contain only
the requested hostname/path conflict class and a safe next action.

TLS supports only `disabled` and `secret_ref`. `secret_ref` is an opaque Opsi
reference resolved through a typed boundary to a verified Opsi-owned
`kubernetes.io/tls` Secret in the workload namespace. The renderer never reads
or returns certificate/private-key data and has no plaintext or file fallback.

## Consequences

- R5-010 namespace/name/label conventions and its exact ClusterIP Service are
  reused; no second deployment engine or Kubernetes client is introduced.
- R5-011.1 is local contract, renderer, and read-only preflight only. It does
  not apply an Ingress or create a live external endpoint.
- Readiness reconciliation, persistence/WAL, known-good rollback, Cloud
  lifecycle/API, Agent exposure commands, CLI/UI, and live endpoint acceptance
  remain R5-011.2 and R5-011.3.
- DNS, certificate provisioning, Cloudflare, cert-manager, and private-key
  handling remain outside this decision.
