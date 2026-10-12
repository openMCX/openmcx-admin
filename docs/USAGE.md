# Using MCX Admin

## The service

`bin/mcxadmin-server --config server.json` serves the `/v1` API ([slice B](SLICE_B.md)): the configuration in
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
