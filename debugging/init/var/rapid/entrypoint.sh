#!/usr/bin/env bash

LAMBDA_INIT_DELVE_PORT="${LAMBDA_INIT_DELVE_PORT:-40000}"

# Run init without delve debugger
#exec /var/rapid/init

exec /var/rapid/dlv --listen=:${LAMBDA_INIT_DELVE_PORT} --headless=true --api-version=2 --accept-multiclient exec /var/rapid/init
