# P07B2 Redis-compatible managed resource

The canonical Opsi resource type and protocol remain `redis`. The experimental
single-node runtime is Valkey `8.1.3-alpine`, pinned server-side to:

`docker.io/valkey/valkey@sha256:5d586b6d9574ce96954142cdca85f4903a0efdbd4d04d4fe27c9fb245cdf91d4`

This MVP is internal-only, ephemeral, non-replicated, and has no PVC,
Sentinel, Cluster, HA, TLS, or Redis module claims. It supports Redis RESP
clients and the tested `PING`, `SET`, and `GET` operations.

Cloud is the credential authority. It generates one `opsi` ACL credential per
resource identity and stores it encrypted in `managed_resource_credentials`.
The credential is reused across reconcile/restart and is not present in the
resource or compiled spec. The Agent materializes a deterministic owned
server Secret containing `username`, `password`, and `valkey.conf`; the
Valkey container mounts it read-only and reads the config from the file.

Application bindings emit non-secret `CACHE_HOST` and `CACHE_PORT`, plus typed
secret references for `CACHE_USER`, `CACHE_PASSWORD`, and `CACHE_URL`. The
application Secret is separate from the server ACL Secret and is materialized
only at deployment time.
