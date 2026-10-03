# The MCX Admin service for openMCX labs: built on the openMCX development image (openMCX/openmcx-server
# docker/Dockerfile.dev), run on a slim Ubuntu image like the servers.
FROM openmcx-dev:0.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -o /install/ ./cmd/...

# Runtime: the two programs are static Go binaries; curl for the health check, Python for the lab tools that run
# their helper in this image (openmcx-server tests/integration/admin_helper.py).
FROM ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl python3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /install/ /usr/local/bin/
COPY LICENSE NOTICE /usr/share/doc/openmcx-admin/
LABEL org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.source="https://github.com/openMCX/openmcx-admin"
ENTRYPOINT ["mcxadmin-server"]
