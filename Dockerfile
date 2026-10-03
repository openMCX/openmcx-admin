# The MCX Admin service for openMCX labs, built and run on the openMCX development
# image like the servers (openMCX/openmcx-server docker/Dockerfile).
FROM openmcx-dev:0.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -o /install/ ./cmd/...

FROM openmcx-dev:0.1
COPY --from=build /install/ /usr/local/bin/
ENTRYPOINT ["mcxadmin-server"]
