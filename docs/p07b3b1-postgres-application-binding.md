# P07B3B1 PostgreSQL application binding

The canonical input remains a `ResourceBinding` with protocol `postgres` and
logical name `DATABASE`. Cloud derives one stable PostgreSQL login from the
binding ID and stores its generated password in the existing encrypted
`managed_resource_credentials` authority with purpose `resource_binding`, the
binding as owner, and the PostgreSQL Resource as resource identity. This is
separate from the Resource's `resource_management` credential.

The compiler emits `DATABASE_HOST`, `DATABASE_PORT`, and `DATABASE_NAME` as
non-secret runtime values. `DATABASE_USER`, `DATABASE_PASSWORD`, and
`DATABASE_URL` are typed references to one binding-owned Secret; plaintext
passwords and URLs are not persisted in `WorkloadSpec`. `DATABASE_NAME`
remains non-secret under the canonical binding contract. The host is the
stable managed PostgreSQL Service DNS identity and the port is `5432`.

The trusted Agent management path creates or reconciles the exact binding role
with `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`.
The role receives `CONNECT` on the canonical database, `USAGE, CREATE` on the
public schema, normal table and sequence DML, and matching default privileges
for future management-owned objects. An application's own migration tables
are owned by that binding role.

Deleting a binding removes its encrypted credential and exact workload
Secret after the Agent changes the role to `NOLOGIN`, terminates sessions, and
revokes database/schema/table/sequence grants. The role is retained when it
owns application tables, so data on the retained PVC is not dropped. Other
binding roles and Secrets are selected by different credential identities and
remain usable. Resource deletion is rejected while any binding remains; after
bindings are removed, P07B3A runtime deletion still retains the PVC.

This milestone does not implement PVC destruction, database/table deletion,
backup, restore, PITR, replication, HA, pooling, public exposure, storage
expansion, or PostgreSQL upgrade automation.
