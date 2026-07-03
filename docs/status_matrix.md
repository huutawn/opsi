# Opsi Status Matrix

| SRS requirement | Status | Evidence | Gap | Issue ID |
|---|---|---|---|---|
| Local-first Agent owns runtime execution/data | Partial | `agent/` deploy, telemetry, secrets, incidents; CLI local status bridge | Local UI still needs more Agent-backed pages | A-01, CS-02, P2-A |
| Continuous deployment from Cloud relay to Agent | Implemented minimum | `agent/internal/cloudrunner`, Cloud lease/result APIs | Progress events are still terminal/coarse | D-01, D-03 |
| Project-scoped operational actions | Implemented minimum | Agent/Cloud requests carry `project_id`; RBAC tests exist | Keep expanding matrix as RPCs grow | SEC-01 |
| PAT verify and role-aware access | Implemented minimum | Cloud bcrypt PAT verify; Agent mutation guards | OAuth/PAT issuance UI missing | SEC-01 |
| Secret reveal gated by Owner + OTP/TOTP | Implemented minimum | Agent secret service + Cloud OTP | Agent-vault HTTP UI endpoints missing | SEC-05 |
| Cloud does not store raw operational payloads | Partial | Telemetry remains local; AI endpoint rejects secret/raw-log keys | Durable webhook relay queue still pending | CS-03, OBS-04, P2-B |
| AI RCA uses sanitized context | Partial | Incident context schema gate; fixture metadata explicit | Gemini/provider adapter not wired | OBS-02, P2-C |
| Audit trail for sensitive actions | Implemented minimum | Cloud/Agent audit paths; Postgres append-only trigger | More UI audit filtering needed | SEC-06 |
| Production packaging | Partial | `Makefile build/release`, version ldflags, embedded CLI UI | Installer/service units not included | DX-03, P2-F |

