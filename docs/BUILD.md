# Building and testing MCX Admin

Everything builds in the OpenMCX development image, with a throwaway MySQL 8.0 for the store and API tests; nothing
is installed on the host:

```bash
tools/test                                                        # vet, build bin/, all tests
MCXADMIN_SITES=../openmcx-server/artifacts/regress-<run> tools/test    # plus the golden check on those site.json files
```

The Docker image `openmcx-admin` builds on the same development image (`openmcx-dev:0.1`, from openmcx-server's
`docker/Dockerfile.dev`), runs `go vet` and `go test`, and ships the two static programs on Ubuntu 24.04:

```bash
docker build -t openmcx-admin:<tag> -f docker/Dockerfile .
```
