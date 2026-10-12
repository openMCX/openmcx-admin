```
  ___                   __  __  ______  __
 / _ \ _ __   ___ _ __ |  \/  |/ ___\ \/ /
| | | | '_ \ / _ \ '_ \| |\/| | |    \  /
| |_| | |_) |  __/ | | | |  | | |___ /  \
 \___/| .__/ \___|_| |_|_|  |_|\____/_/\_\
      |_|
```

# MCX Admin — administration of the OpenMCX MCx system

The project **OpenMCX** implements **3GPP Release 19 mission-critical services (MCx)** for an IP network. This
repository holds **MCX Admin**, its administration: accounts, MC identities, MCPTT, MCData and MCVideo rights, policy
templates and groups, created one by one in a web console or in bulk from a CSV or XLSX spreadsheet, and the
activation of the clients. It handles no secrets (no passwords, certificates or keys) and never changes how the
OpenMCX servers behave: they keep their 3GPP interfaces and read their data from here.

This is the **standalone edition**: MCX Admin administers one MC system. The servers are in
[openmcx-server](https://github.com/openMCX/openmcx-server) and the clients in
[openmcx-client](https://github.com/openMCX/openmcx-client).

**Containerised deployment** on Ubuntu 24.04 LTS, built in Docker ([building](docs/BUILD.md)).

**Try it**: [Tutorial 1: MCX over WLAN with the Android app](https://github.com/openMCX/openmcx-server/blob/main/tutorials/end-to-end/01-mcx-over-wlan-android/README.md)
runs the whole system, MCX Admin included, on one computer from the Docker Hub images; its users, groups and phone
activations are made in MCX Admin ([all tutorials](https://github.com/openMCX/openmcx-server/blob/main/tutorials/README.md)).

## Features

- Web console and `/v1` API: MC users, identities, service rights, policy templates and groups
- Bulk upload from a CSV or XLSX spreadsheet, with a change preview and per-row errors; nothing is applied while a
  row is wrong
- Every change a revision, with audit trail, export and rollback; administrators sign in with an IdMS access token
- Client activation: one-time links and QR codes for the Android app and the dispatcher
- Carries each revision to the servers: the user database (Diameter), the Credential Service and the group management
  server (GMS)

## Documentation

- **[Using MCX Admin](docs/USAGE.md)**: the service and the command-line tools
- **[Operations](docs/OPERATIONS.md)**: run, back up, restore and watch MCX Admin
- **Design**: [slice A](docs/SLICE_A.md) (configuration model), [slice B](docs/SLICE_B.md) (store, API, sign-in), [slice C](docs/SLICE_C.md) (store live)
- **[Coding style](docs/CODING_STYLE.md)**
- **[Tutorials](https://github.com/openMCX/openmcx-server/blob/main/tutorials/README.md)**: users, groups and activations made in MCX Admin
- **[3GPP correction candidates](https://github.com/openMCX/openmcx-server/blob/main/docs/3GPP_CORRECTION_CANDIDATES.pdf)**: Release 19, AI-assisted review, awaiting expert confirmation

## Licence

MCX Admin is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE).
Third-party software and its licences: [NOTICE](NOTICE).

Contact: [contact@openmcx.com](mailto:contact@openmcx.com)
