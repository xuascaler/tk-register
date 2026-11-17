#!/usr/bin/env bash
set -Eeuo pipefail

backend_pid=""
proxy_pid=""
helper_pid=""

shutdown() {
  trap '' TERM INT
  if [[ -n "$proxy_pid" ]]; then
    kill -QUIT "$proxy_pid" 2>/dev/null || true
  fi
  if [[ -n "$helper_pid" ]]; then
    kill -TERM "$helper_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    kill -TERM "$backend_pid" 2>/dev/null || true
  fi
  if [[ -n "$proxy_pid" ]]; then
    wait "$proxy_pid" 2>/dev/null || true
  fi
  if [[ -n "$helper_pid" ]]; then
    wait "$helper_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    wait "$backend_pid" 2>/dev/null || true
  fi
}

trap 'shutdown; exit 143' TERM
trap 'shutdown; exit 130' INT

# This secret only authenticates container-local helper traffic. Generate it per
# container start unless operators explicitly provide a shared value. Never log it.
if [[ -z "${TK_REGISTER_CREDENTIAL_HELPER_TOKEN:-}" ]]; then
  TK_REGISTER_CREDENTIAL_HELPER_TOKEN="$(node -e 'process.stdout.write(require("node:crypto").randomBytes(32).toString("hex"))')"
fi
if [[ "${#TK_REGISTER_CREDENTIAL_HELPER_TOKEN}" -lt 32 ]]; then
  echo "TK_REGISTER_CREDENTIAL_HELPER_TOKEN must contain at least 32 characters." >&2
  exit 1
fi
export TK_REGISTER_CREDENTIAL_HELPER_TOKEN
export TK_REGISTER_CREDENTIAL_HELPER_URL="http://127.0.0.1:3003"

# Validate certificates and proxy configuration before starting the services.
nginx -t

node /opt/tk-register-credential-helper/src/server.mjs &
helper_pid=$!
/usr/local/bin/tk-register-server &
backend_pid=$!
nginx -g 'daemon off;' &
proxy_pid=$!

# A failed helper, backend, or proxy must restart the entire container, not leave a
# partially functioning service running. Tini reaps orphaned child processes.
set +e
wait -n "$helper_pid" "$backend_pid" "$proxy_pid"
status=$?
set -e
shutdown
if [[ "$status" -eq 0 ]]; then
  status=1
fi
exit "$status"
