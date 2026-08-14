# P07B3A PostgreSQL single-node managed resource

The canonical Opsi resource type and protocol are `postgres`. The initial
`single-node-experimental` runtime is PostgreSQL `18.6`, using the official
Debian Bookworm image variant pinned to the linux/amd64 manifest:

`docker.io/library/postgres:18.6-bookworm@sha256:b939b3851e2cccb017dc4497af63b15e34efa57fba036548773c53b2f16a8871`

Bookworm is used instead of Alpine because the Debian package ecosystem is a
more conservative base for future PostgreSQL extension compatibility. Cloud
owns this version and digest; the Agent does not select or upgrade images.

The runtime is internal-only and consists of one StatefulSet, one ClusterIP
Service, one deterministic standalone PVC, one server credential Secret, and
one Pod. Storage is mandatory and the compiled storage authority contains
`persistent: true`, a positive bounded `size_bytes`, and the typed `default`
storage policy. Kubernetes resolves that policy to the cluster's factual
default StorageClass and records the bound PVC and PV in runtime evidence.
PVC identity and storage intent are tied to the exact project, environment,
resource identity, resource type, requested size, and policy.

Cloud generates and encrypts one stable username, password, and database name
per Resource. The Agent materializes them only in the owned Secret. The
official image reads initialization values from mounted files; readiness uses
`pg_isready` plus authenticated `SELECT 1` without placing the password in
arguments, annotations, topology, or ManagedResourceSpec.

The PVC is mounted at `/var/lib/postgresql`, with PostgreSQL 18 data at
`/var/lib/postgresql/18/docker`. Same-spec reconcile, Pod recreation, Agent
restart, and CPU/memory rollout reuse the same PVC and credentials. Storage
growth, shrink, policy changes, and PostgreSQL version changes fail closed for
this milestone. Runtime deletion removes the StatefulSet, Service, and Secret
but retains the exactly owned PVC; P07B3B will define explicit destructive
storage UX. Backup, restore, replication, HA, and public exposure are absent.
