#!/usr/bin/env bash
# e2e smoke test: starts the ls-api mock and the RIE in Docker, then verifies that
# both a successful and a failing Lambda invocation complete correctly.
# Exits 0 on success, non-zero on failure. Cleans up on exit.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

MOCK_PORT=48490
LS_API_BIN="$REPO_ROOT/bin/ls-api"

LOG_FILE=$(mktemp -t ls-api-smoke.XXXXXX)
CID_FILE=$(mktemp -t rie-smoke.XXXXXX)

cleanup() {
    local cid
    cid=$(cat "$CID_FILE" 2>/dev/null || true)
    if [ -n "$cid" ]; then
        docker stop "$cid" 2>/dev/null || true
        docker rm -f "$cid" 2>/dev/null || true
    fi
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
# Docker flags are defined once in the Makefile (RIE_DOCKER_OPTS) and shared with start-rie.
echo ">>> Starting RIE in Docker"
CID=$(make -s --no-print-directory -C "$SCRIPT_DIR" start-rie-detached)
echo "$CID" > "$CID_FILE"
echo ">>> RIE container: $CID"

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
