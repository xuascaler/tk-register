#!/usr/bin/env bash
set -Eeuo pipefail

backend_pid=""
proxy_pid=""

shutdown() {
  trap '' TERM INT
  if [[ -n "$proxy_pid" ]]; then
    kill -QUIT "$proxy_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    kill -TERM "$backend_pid" 2>/dev/null || true
  fi
  if [[ -n "$proxy_pid" ]]; then
    wait "$proxy_pid" 2>/dev/null || true
  fi
  if [[ -n "$backend_pid" ]]; then
    wait "$backend_pid" 2>/dev/null || true
  fi
}

trap 'shutdown; exit 143' TERM
trap 'shutdown; exit 130' INT

# Validate certificates and proxy configuration before starting either service.
nginx -t

/usr/local/bin/tk-register-server &
backend_pid=$!
nginx -g 'daemon off;' &
proxy_pid=$!

# A failed backend or proxy must restart the entire container, not leave a
# partially functioning service running. Tini reaps orphaned child processes.
set +e
wait -n "$backend_pid" "$proxy_pid"
status=$?
set -e
shutdown
if [[ "$status" -eq 0 ]]; then
  status=1
fi
exit "$status"
