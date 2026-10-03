# Slice A: the configuration model, importer, export and diff

Status: done (model, importer, export, diff, and the spreadsheet path). Design: MySQL (own `mcxadmin` database) for the store of slice B; a single MC system; CSV and XLSX uploads.

## Goal

One versioned data model for MC user profiles and groups, used by the API, the uploads and the importer, and
proof that it carries today's configuration without loss: a `site.json` imported and turned back gives the same users
and groups the servers read, so admin mode can serve the same documents as file mode (3GPP conformance guard, rule 5).

Slice A has no database, no service and no UI. It is a Go library with a command-line tool.

## The configuration document

`schema/config-v1.schema.json` (JSON Schema 2020-12) defines it:

```json
{
  "schema_version": 1,
  "system": {"domain": "mcx.test"},
  "templates": {"field-user": {"private_call": {"allow": true, "automatic_commencement": true}}},
  "users": [{
    "login": "alice", "display_name": "Alice", "email": "", "status": "active",
    "template": "field-user",
    "mc_id": "sip:alice.mc@mcx.test", "mcptt_id": "sip:alice.mcptt@mcx.test", "sip_uri": "sip:alice@mcx.test",
    "client_ids": ["urn:uuid:..."], "mcdata_id": "sip:alice.mcdata@mcx.test", "mcvideo_id": "",
    "groups": ["sip:operations@mcx.test"],
    "profile": {"private_call": {"targets": ["sip:bob.mcptt@mcx.test"]}}
  }],
  "groups": [{"id": "sip:operations@mcx.test", "members": ["sip:alice.mcptt@mcx.test"], "protect_media": false,
              "protect_floor": false}]
}
```

- **Profile.** The rights of a user are the sections the servers read today (`private_call`, `adhoc_call`,
  `group_call`, `mcdata`, `mcvideo`, `location`, `group_management`, the booleans and limits), kept as they are, so no
  right is reinterpreted. A user's effective profile is its template with its own `profile` merged over it: objects
  merge key by key, other values replace.
- **Groups** are the group records of today, unchanged.
- **No secrets.** A document that holds a password, secret, key or certificate is rejected.

## Validation

Every identity is unique across all users (login, MC ID, MCPTT ID, SIP URI, MCData ID, MCVideo ID, client IDs), and
every ID is a `sip:` URI except client IDs. A user's template exists; its groups exist; a group's members are known
MCPTT IDs (MCVideo members known MCVideo IDs). Errors name the record and field, so an upload can report them per row.

## Commands (`mcxadmin`)

| Command | Does |
|---|---|
| `mcxadmin import <site.json>` | Writes the configuration document of a `site.json`, and a report of what it removed (secrets) |
| `mcxadmin validate <config.json>` | Validates a document; exit 1 with the errors |
| `mcxadmin diff <before.json> <after.json>` | The change preview: users and groups added, removed and changed, each right gained or lost, members added or removed |
| `mcxadmin to-site <config.json> <base site.json>` | The users and groups as the servers read them, secrets taken from the base: the golden check |

## Evidence

- Unit tests on a synthetic `site.json` covering every user and group section.
- Golden: for the `site.json` of every lab in a regression (single-system and inter-system), `to-site(import(x), x)`
  gives users and groups equal to those of `x`.
- Validation: duplicate identities, unknown group or template, a secret in a document.
- Diff: each kind of change is reported once, and a document compared with itself reports none.

## The spreadsheet path

For people onboarding many users, a CSV or XLSX spreadsheet is the bulk format; JSON stays the machine
format. `mcxadmin template` gives the file to fill in: an XLSX workbook with a `users` sheet, a `groups` sheet and a
`help` sheet listing each column and the system's templates, or `users.csv` and `groups.csv`.

| Sheet | Columns (* required) |
|---|---|
| users | `login`*, `display_name`, `email`, `template`*, `groups`, `services`, `status`, and optionally `mc_id`, `mcptt_id`, `sip_uri`, `mcdata_id`, `mcvideo_id` |
| groups | `id`*, `services`, `members` (logins) |

- **Rights come only from the template** a row names; a spreadsheet never carries individual rights, which keeps
  thousands of users consistent. Per-user adjustments are made in the UI.
- **Identities are generated** from the login and the system's domain (`alice.mc`, `alice.mcptt`, `alice`,
  `alice.mcdata`, `alice.mcvideo`) unless a cell gives them, and kept once set. The client ID is a name-based UUID of
  the domain and login, so uploading the same sheet again changes nothing.
- **Nothing is removed** by an upload: a user leaves with status `retired`. A group row replaces its members.
- **Errors are per row** (sheet, line, column, reason). With any error nothing is applied, and the sheets come back
  with an `error` column to fix and upload again. Lists in a cell are separated by `;`.

Evidence: tests for creation, generated identities, idempotent rerun, each kind of row error, the header check
(including Excel's byte-order mark), XLSX giving the same result as CSV, and 10,000 users read, applied and validated
in about 60 ms.
