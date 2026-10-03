# Coding style

This repository follows the openMCX coding style: openmcx-server `docs/CODING_STYLE.md` (names, 3GPP references,
separators, file and function size, formatting, tools). The Go configuration is the same (`.golangci.yml`), and so
are the commands:

| Command | What it does |
|---|---|
| `tools/format [--check]` | gofmt |
| `tools/lint [--strict]` | golangci-lint (no single-letter names, function size and complexity, unchecked errors, vet, staticcheck) |

Both run in the `openmcx-lint:0.1` image, built by openmcx-server's `tools/lint-image`, with the Go module cache in the
Docker volume `mcxadmin-gopath` (as `tools/test`).
