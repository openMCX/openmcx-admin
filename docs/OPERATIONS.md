# Operating MCX Admin

How to run, back up, restore and watch MCX Admin. MCX Admin stores no password, key, certificate or token: its database holds the configuration revisions, the change outbox, the audit trail, one-time activation codes (as SHA-256 hashes only) and the GMS versions of the groups it wrote.

## What runs

- `mcxadmin-server`: one process, the `/v1` API, the web console and the consumers that carry each revision to the services: `userdb` (the Diameter user database's file), `sip` (the Credential Service) and `gms` (the group management server). Its database is `mcxadmin` on the MySQL the SIP core uses; the Docker image is `openmcx-admin`.
- `/healthz` answers 200 once the database is reachable and the user database's file is written; the container health check uses it.

## Backup

```
tools/backup <mysql container> /backups/mcxadmin-$(date +%F).sql
```

A consistent snapshot (`mysqldump --single-transaction`) of the whole store. It can be taken while MCX Admin runs. `tools/test-backup` proves a backup restores the same rows in every table and holds no secret.

Back up after bulk changes and at least daily. The services' own state is not in it: IdMS keeps password verifiers, the Credential Service the SIP digest hashes, the GMS its group documents; back those up with their services.

## Restore

1. Stop `mcxadmin-server`.
2. `tools/restore <mysql container> /backups/mcxadmin-<date>.sql` (replaces the `mcxadmin` database).
3. Start `mcxadmin-server`. It applies newer migrations, and its consumers apply what the outbox still holds.

Every consumer's step is idempotent, so replaying a revision is safe: the user database's file is rewritten whole; the Credential Service's suspend, resume and retire leave an account as they found it when repeated; the GMS does not make a new version for an unchanged group.

**Restoring an older backup.** Changes made after the backup are lost from MCX Admin, but the services may still hold them: the GMS may have newer group documents, and the importer then takes them in as revisions with source `xcap`, so group changes made after the backup come back under that source. Users created after the backup are unknown to IdMS, the CMS and the user database again, and are refused at sign-in. After such a restore, review the History and Audit views and export the configuration.

## Restart and upgrade

`mcxadmin-server` can be restarted at any time: a revision is committed with its outbox rows in one transaction, and the consumers resume from the outbox. During a restart, sign-in to the services that read accounts from MCX Admin (IdMS, the CMS, the GMS and KMS in admin mode) is refused: keep restarts short. Run one instance: a second one on the same database would run the consumers twice, which is not yet supported (the store serialises writes, but high availability is untested).

A consumer added in an upgrade (for example `gms` through `projections.gms_url`) starts from the latest revision on its first run.

## Watch

- The dashboard: services' health, change delivery per consumer (applied revision, pending and failed counts), open activation links and recent activity.
- `GET /v1/health/projections` and `GET /v1/status` for monitoring: alert when a consumer's applied revision stays behind the current one, or has failed rows.
- The Audit view (`GET /v1/audit?latest=N`): refused sign-ins, issued links and activations, changes, and imports refused (`xcap-import-refused`).

## Capacity (measured 2026-09-30)

`MCXADMIN_LOAD=1 tools/test` runs the load profile on a throwaway MySQL 8.0 on one development host:

| 10,000 users | Time |
| --- | --- |
| Upload checked (CSV, validate) | 11.1 s |
| Upload applied (20 batches of 500, one revision each) | 23.5 s |
| Search by login, p50 / p95 | 122 ms / 199 ms |
| A suspension committed with its outbox rows | 1.1 s |

The consumers then carry the suspension to the services within their polling interval (1 s). Every revision stores the whole configuration document, so the cost of a write grows with the size of the site: at 10,000 users it is about a second, and a much larger site would need revisions stored as changes.
