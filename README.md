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
- `tk-register-web-portal/credential-helper/`: private Claude authorization and JSON import helper.
- `tk-register-web-portal/tests/`: frontend and integration tests.
- `Dockerfile`, `nginx.conf`, `start.sh`: standalone HTTPS runtime.
- `Dockerfile.aws`, `nginx.aws.conf`, `aws-rds-ca/`: AWS source-build image and public Tokyo RDS trust roots.

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
server on container loopback port `3002`. A Node.js helper listens only on
`127.0.0.1:3003`; it is not published or proxied. The startup script generates an
internal shared secret and supervises all three processes together. Mount the TLS certificate and private
key directory at `/etc/tk-register/tls` read-only. Required private environment
settings include `TK_REGISTER_SQL_DSN`, `TK_REGISTER_REDIS_URL`,
`TK_REGISTER_ENCRYPTION_KEY`, `TK_REGISTER_ADMIN_EMAIL`, and
`TK_REGISTER_PUBLIC_ORIGIN`. Preserve the existing encryption key when upgrading.

Building an image or editing source does not update a running container. The
production Compose configuration must explicitly select the new image before
recreating the service. This repository import does not deploy a new version.

See [the application documentation](tk-register-web-portal/README.md) for
authentication, vendor protocols, API token usage, and configuration details.

## AWS source-build image

The AWS deployment builds the frontend, Go server, and credential helper from
source instead of using the prebuilt root artifacts. From the repository root:

```sh
docker build -f Dockerfile.aws \
  --build-arg APP_REVISION="$(git rev-parse HEAD)" \
  -t tk-register:aws .
```

The image adds the public Amazon RDS Tokyo root certificates to the system trust
store. These are CA certificates, not the service's private HTTPS certificate or
key. The runtime still uses HTTPS port `8443`, private environment settings, and
the read-only TLS mount described above. Host-specific Compose files, environment
values, and private keys remain outside Git.

Two operator-only initialization commands are included in the image. Neither is
run at startup or exposed through an HTTP route:

- `tksmtpbootstrap export-smtp` reads only the nine SMTP settings and writes JSON
  to a pipe or protected file. The output contains plaintext mail credentials and
  must never be logged or committed. `init-smtp` reads that JSON from standard
  input, validates it, and encrypts secret settings with the target encryption
  key. Initialization requires `TK_SMTP_BOOTSTRAP_CONFIRM_FRESH=1` and
  `TK_SMTP_BOOTSTRAP_EXPECTED_HOST` to match the target DSN host; it refuses
  existing SMTP configuration or business data. Run it before exposing the new
  service to users.
- `tkadminbootstrap` reads an email and password from JSON on standard input. It
  requires an empty users table, `TK_ADMIN_BOOTSTRAP_CONFIRM_EMPTY=1`, and
  `TK_ADMIN_BOOTSTRAP_EXPECTED_HOST` matching the target DSN host. The email must
  match `TK_REGISTER_ADMIN_EMAIL`. It creates a verified administrator with a
  bcrypt password hash and the required self-membership row; it cannot reset an
  existing user's password. Normal administrator sign-in still requires the
  password and email verification code.

## Initial import

The initial source import matches the deployed
`tk-register:standalone-20261030-api-token` release, using the deployment's
`source-current.tar.gz` snapshot. Later releases add saved account usage, trends, automatic supplier sign-in, and
configurable collection (two hours by default). Administrator Accounts shares
the same usage view across all users and adds owner attribution. See
[account sync documentation](tk-register-web-portal/docs/account-sync.md).

The parent project's GNU AGPL v3 license is preserved in `LICENSE`.

## Authorization and submission

**Submit account → Authorize & submit** adds proxy-bound OAuth authorization,
Setup Token, and JSON import alongside the existing manual form. See
[authorization workflow](tk-register-web-portal/docs/authorize-submit.md) for the
defaults, credential handling, and deployment requirements.
