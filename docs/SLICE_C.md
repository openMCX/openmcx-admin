# Slice C: the store goes live

Status: done 2026-09-29.

| Consumer | How it follows the store | Where |
|---|---|---|
| IdMS | Asks for the account at sign-in and introspection (B2) | openmcx-server `idms_accounts.go` |
| CMS | Asks for the user record (identities and effective profile, active only) behind the profile documents and the servers' `/internal/user` | openmcx-server `cms_accounts.go`; here `GET /internal/v1/site-users` |
| Diameter user database | The `userdb` outbox consumer writes its users file after each revision, active accounts only; `mcx-user-db --watch` reloads it | here `internal/project`; openmcx-server `src/user_db.cpp`, `DiameterProfileServer::replace_profiles` |

A suspended user is refused only by standard means: IdMS sign-in and introspection, the CMS answering "unknown", the
user database answering "user unknown" (TS 29.283). The SIP subscriber table stays as generated until the Credential
Service (slice F1). The CMS's own XCAP profile overrides (TS 24.484 §6.3.4) keep working as before; turning them into
revisions here is slice D.

## The admin lab

`openmcx-server` `tools/prepare-prearranged-test.py --admin --admin-image <image>` (and the `admin` lab of
`tools/regress.py`): this service in the lab, with its own database on the lab's MySQL, seeded from the lab's
`site.json`; IdMS, the CMS and the user database in admin mode. `docker build -t openmcx-admin:<tag> .` builds the
image.

Evidence (openmcx-server `tests/integration/admin_scenario.py`, ADM-01 to ADM-04): the clients authorise and call in admin
mode; a non-administrator is refused; a suspension refuses IdMS sign-in, drops the CMS record and the Diameter profile,
and a restarted client is refused; a restore brings everything back; the revisions and the audit name the
administrator, and no token or password is stored. Passed over TCP and TLS (`artifacts/regress-c1-admin-both/`).
