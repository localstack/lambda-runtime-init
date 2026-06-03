#!/usr/bin/env bash
# e2e smoke test: starts the ls-api mock and the RIE in Docker, then verifies that
# both a successful and a failing Lambda invocation complete correctly.
# Exits 0 on success, non-zero on failure. Cleans up on exit.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

MOCK_PORT=48490
INTEROP_PORT=9563
RIE_BINARY="$REPO_ROOT/bin/aws-lambda-rie-x86_64"
LS_API_BIN="$REPO_ROOT/bin/ls-api"

LOG_FILE=$(mktemp -t ls-api-smoke.XXXXXX)
CID_FILE=$(mktemp -t rie-smoke.XXXXXX)

cleanup() {
    local cid
    cid=$(cat "$CID_FILE" 2>/dev/null || true)
    [ -n "$cid" ] && docker stop "$cid" 2>/dev/null || true
    [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null || true
    rm -f "$LOG_FILE" "$CID_FILE"
}
trap cleanup EXIT

wait_for_log() {
    local pattern="$1" timeout_sec="$2" elapsed=0
    while (( elapsed < timeout_sec )); do
        grep -q "$pattern" "$LOG_FILE" && return 0
        sleep 1
        elapsed=$(( elapsed + 1 ))
    done
    echo "ERROR: timed out after ${timeout_sec}s waiting for '${pattern}'" >&2
    local cid
    cid=$(cat "$CID_FILE" 2>/dev/null || true)
    if [ -n "$cid" ]; then
        echo "--- RIE container status ---" >&2
        docker inspect "$cid" --format='status={{.State.Status}} exit_code={{.State.ExitCode}}' 2>/dev/null >&2 || true
        echo "--- RIE container logs ---" >&2
        docker logs "$cid" 2>&1 >&2 || true
    fi
    echo "--- ls-api log ---" >&2
    cat "$LOG_FILE" >&2
    return 1
}

# ---- start mock ----
echo ">>> Starting ls-api mock (port $MOCK_PORT)"
"$LS_API_BIN" > "$LOG_FILE" 2>&1 &
MOCK_PID=$!
for i in $(seq 1 10); do nc -z localhost $MOCK_PORT 2>/dev/null && break || sleep 1; done

# ---- start RIE ----
echo ">>> Starting RIE in Docker"

# Docker Desktop on macOS resolves host.docker.internal natively.
# On Linux use the Docker bridge gateway IP directly.
if [[ "$(uname -s)" == "Darwin" ]]; then
    MOCK_ENDPOINT="http://host.docker.internal:$MOCK_PORT"
else
    MOCK_ENDPOINT="http://172.17.0.1:$MOCK_PORT"
fi

docker_opts=(
    --rm --detach
    -p "$INTEROP_PORT:$INTEROP_PORT"
    -v "$RIE_BINARY:/var/rapid/init:ro"
    -v "$SCRIPT_DIR/handler.py:/var/task/handler.py:ro"
    -e "LOCALSTACK_RUNTIME_ENDPOINT=$MOCK_ENDPOINT"
    -e "LOCALSTACK_RUNTIME_ID=smoke-test-runtime"
    -e "AWS_LAMBDA_FUNCTION_TIMEOUT=30"
    -e "AWS_LAMBDA_FUNCTION_MEMORY_SIZE=128"
    -e "AWS_REGION=us-east-1"
    -e "_HANDLER=handler.handler"
    --entrypoint /var/rapid/init
)

CID=$(docker run "${docker_opts[@]}" public.ecr.aws/lambda/python:3.12)
echo "$CID" > "$CID_FILE"
echo ">>> RIE container: $CID (endpoint: $MOCK_ENDPOINT)"

# ---- verify success invocation ----
# The mock auto-fires one invocation as soon as it receives POST /status/{id}/ready from the RIE.
echo ">>> Waiting for success invocation (auto-triggered on ready)..."
wait_for_log "invokeResponseHandler" 30
echo ">>> Success invocation received"

# ---- verify error invocation ----
echo ">>> Triggering error invocation..."
curl -sf "http://localhost:$MOCK_PORT/fail"
wait_for_log "invokeErrorHandler" 15
echo ">>> Error invocation received"

echo ""
echo "=== Smoke test passed: success + error invocations verified ==="
echo ""
echo "--- ls-api log ---"
cat "$LOG_FILE"
