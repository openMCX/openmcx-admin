# Slice B: the store, the API and administrator sign-in

Status: B1 and B2 done. Split in two:

- **B1 (this repository):** the MySQL store with revisions, audit and outbox; the `/v1` API with optimistic
  concurrency; uploads applied in batches; export and rollback; administrators signed in through IdMS.
- **B2 (`openmcx-server`):** IdMS password hashing, set-password and reset links, and account status
  from the store. It changes no 3GPP behaviour (conformance guard).

## Store (MySQL, database `mcxadmin` on the shared engine)

| Table | Holds |
|---|---|
| `revisions` | Every applied change: its number, time, actor, reason, source (`api`, `upload`, `import`, `rollback`), the whole document after it, and its change list (the diff) |
| `audit` | Append-only: who did what to which record, with the revision; never a token or secret |
| `outbox` | One row per revision and consumer (`sip`, `userdb`, later `gms`), status `pending`, `applied` or `failed`; written in the same transaction as the revision. Slice C adds the consumers |
| `schema_migrations` | Applied migrations (embedded SQL, run at start) |

The current configuration is the latest revision's document. A write names the revision it was based on
(`If-Match`); if another write came first, it fails with 412 and nothing changes. A revision is committed only if
the document validates.

## API

| Method and path | Does |
|---|---|
| `GET /v1/config` | The current document; `ETag` is its revision |
| `GET /v1/users?q=&status=&offset=&limit=` | Search and page users |
| `GET /v1/users/{login}` | One user, with its effective profile |
| `PUT /v1/users/{login}` | Create or replace a user (`If-Match` required) |
| `POST /v1/users/{login}/suspend`, `/restore` | Change status (`If-Match` required) |
| `POST /v1/uploads?mode=validate` | A spreadsheet (XLSX, or CSV users) checked: per-row problems and the change preview; nothing applied |
| `POST /v1/uploads?mode=apply` | The same, then applied in batches of 500 users, one revision each, groups in the last |
| `GET /v1/upload-template.xlsx` | The workbook to fill in |
| `GET /v1/revisions?after=` | Revisions with their change lists |
| `GET /v1/revisions/{n}/export` | The document of revision n (backup, diff between revisions) |
| `POST /v1/revisions/{n}/rollback` | A new revision with revision n's document (`If-Match` required); `?preview=1` shows the change only |
| `GET /v1/audit?after=` | The audit trail |
| `GET /healthz` | Liveness and database reachability |

## Administrator sign-in

An administrator is an MC user whose effective profile grants `config_management` (TS 24.484 §6.3.4, the MC service
administrator right the servers already know). The API takes an IdMS access token (`Authorization: Bearer`), checks it
with IdMS introspection (the server client credentials IdMS already has), and admits the MCPTT ID it names only if
that user is `active` with `config_management`. MCX Admin stores no administrator password; the token is never
logged. Multifactor sign-in comes with the admin UI (E1) through IdMS.

## Evidence

Tests against MySQL 8.0 (the lab's `sip-db` image) in a container: migrations from empty; a write committed with
its audit and outbox rows in one transaction; a stale `If-Match` refused; two concurrent writes, one refused; an
invalid document refused; an upload in batches; export and rollback; sign-in refused for an inactive token, an
unknown user and a user without `config_management`; no token or secret in the store or the audit.

## B2: IdMS

With `admin_accounts` (MCX Admin's URL and the base URL of its links) in IdMS's configuration:

- **Accounts:** at sign-in and at introspection, IdMS asks MCX Admin for the account (`GET /internal/v1/accounts`, with
  the server client credentials). Only an `active` account signs in, and a suspended account's tokens are inactive at
  once.
- **Passwords:** IdMS keeps a salted PBKDF2-HMAC-SHA256 verifier (600,000 iterations) in `idms-credentials.json`
  (mode 0600) in its data directory; 10 failed sign-ins lock a login for 15 minutes. MCX Admin never sees a password.
- **Links:** `POST /v1/users/{login}/password-link` here asks IdMS (`POST /internal/password-links`) for a one-time
  link valid 72 hours, returned once and stored by IdMS only as a SHA-256 hash; the user sets the password (12 to 256
  characters) on IdMS's page.

Without `admin_accounts`, IdMS is unchanged (file mode). The OIDC flow and its tokens are unchanged (TS 24.482).
