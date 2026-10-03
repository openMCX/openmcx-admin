```
                            __  __   ____ __  __
  ___   _ __    ___  _ __  |  \/  | / ___|\ \/ /
 / _ \ | '_ \  / _ \| '_ \ | |\/| || |     \  /
| (_) || |_) ||  __/| | | || |  | || |___  /  \
 \___/ | .__/  \___||_| |_||_|  |_| \____|/_/\_\
       |_|
```

# openMCX Admin

MC user profile and group administration for an openMCX system: accounts, MC identities, MCPTT, MCData and MCVideo
rights, policy templates and groups, created one by one in a web UI or in bulk from a CSV or XLSX spreadsheet. It
handles no secrets (no passwords, certificates or keys) and never changes how the openMCX servers behave: they keep
their 3GPP interfaces and read their data from here.

The design is documented in [slice A](docs/SLICE_A.md) (the configuration model, the `site.json` importer, export and the change preview), [slice B](docs/SLICE_B.md) (store, API, sign-in) and [slice C](docs/SLICE_C.md) (the store goes live).

## Build and test

Everything builds in the openMCX development image, with a throwaway MySQL 8.0 for the store and API tests; nothing
is installed on the host:

```bash
tools/test                                                        # vet, build bin/, all tests
MCXADMIN_SITES=../openmcx-server/artifacts/regress-<run> tools/test    # plus the golden check on those site.json files
```

## The service

`bin/mcxadmin-server --config server.json` serves the `/v1` API ([slice B](docs/SLICE_B.md)): the configuration in
MySQL as revisions with audit and outbox, uploads in batches, export and rollback. Administrators sign in with an IdMS
access token; they are MC users granted `config_management`. `--import site.json` seeds an empty store once.

## Commands

```bash
bin/mcxadmin import site.json > config.json      # the document; what was removed or kept aside on stderr
bin/mcxadmin validate config.json                # unique identities, known groups and templates, no secrets
bin/mcxadmin diff before.json after.json         # the change preview
bin/mcxadmin to-site config.json site.json       # users and groups as the servers read them
bin/mcxadmin template config.json upload.xlsx    # the spreadsheet to fill in (or a directory: users.csv, groups.csv)
bin/mcxadmin sheet config.json upload.xlsx rep/  # the new document and the change preview; with row errors,
                                                 # nothing is applied and rep/ gets the sheets with an error column
```

The document format is `schema/config-v1.schema.json`.

## Licence

MCX Admin is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE).
Third-party software and its licences: [NOTICE](NOTICE).
