# TK Register

Standalone account submission portal with email authentication, administrator
configuration, vendor account submission and history, billing queries, user role
management, an administrator account pool, and personal API tokens.

This repository contains the TK Register component extracted from
[GoAPI](https://github.com/xuascaler/goapi). Its frontend and Go server are
self-contained; no parent GoAPI source checkout is required.

## Source layout

- `tk-register-web-portal/src/`: React and TypeScript frontend.
- `tk-register-web-portal/server/`: independent Go module and backend tests.
- `tk-register-web-portal/tests/`: frontend and integration tests.
- `Dockerfile`, `nginx.conf`, `start.sh`: standalone HTTPS runtime.

The production working directory is `/root/Code/tk_register`. Application source
is under `tk-register-web-portal/`. The local `compose.yaml`, `.env`, TLS material,
backups, staging files, archives, and compiled output are deliberately untracked.
Do not replace those files when updating the repository.

## Validation

Use Node.js 22.13 or newer and Go 1.26.1 or newer:

```sh
npm --prefix tk-register-web-portal ci
npm --prefix tk-register-web-portal test
npm --prefix tk-register-web-portal run build
cd tk-register-web-portal/server
go test ./...
go vet ./...
```

Tests use local fixtures. Live vendor credentials are not needed for the default
test suite. Optional browser and external Redis integration checks are documented
in the application README and their test files.

## Standalone image

The Dockerfile packages prebuilt artifacts. From the repository root:

```sh
npm --prefix tk-register-web-portal ci
npm --prefix tk-register-web-portal run build
mkdir -p dist
cp -R tk-register-web-portal/dist/. dist/
(cd tk-register-web-portal/server && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ../../tk-register-server .)
docker build -t tk-register:local .
```

The image runs Nginx on HTTPS port `8443` and proxies `/tk-register` to the Go
server on container loopback port `3002`. Mount the TLS certificate and private
key directory at `/etc/tk-register/tls` read-only. Required private environment
settings include `TK_REGISTER_SQL_DSN`, `TK_REGISTER_REDIS_URL`,
`TK_REGISTER_ENCRYPTION_KEY`, `TK_REGISTER_ADMIN_EMAIL`, and
`TK_REGISTER_PUBLIC_ORIGIN`. Preserve the existing encryption key when upgrading.

Building an image or editing source does not update a running container. The
production Compose configuration must explicitly select the new image before
recreating the service. This repository import does not deploy a new version.

See [the application documentation](tk-register-web-portal/README.md) for
authentication, vendor protocols, API token usage, and configuration details.

## Initial import

The initial source import matches the deployed
`tk-register:standalone-20261030-api-token` release, using the deployment's
`source-current.tar.gz` snapshot. The later Accounts / Account sync interface is
still a design preview and is not included as implemented functionality here.

The parent project's GNU AGPL v3 license is preserved in `LICENSE`.
