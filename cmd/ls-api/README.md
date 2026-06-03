# ls-api — LocalStack endpoint mock

A lightweight HTTP server that stands in for the LocalStack endpoint when testing the RIE in isolation, without a running LocalStack instance.

## Ports

| Port | Direction | Purpose |
|------|-----------|---------|
| `48490` | inbound | Receives callbacks from the RIE (logs, response, status) |
| `9563` | outbound | Sends invocations to the RIE's `/invoke` endpoint |

## How to use

**Terminal 1 — start the mock:**

```bash
go run ./cmd/ls-api
```

**Terminal 2 — start the RIE** pointing at the mock instead of LocalStack:

```bash
LOCALSTACK_RUNTIME_ENDPOINT=http://localhost:48490 \
LOCALSTACK_RUNTIME_ID=test-runtime-id \
./bin/aws-lambda-rie-x86_64 python3 -m awslambdaric handler.handler
```

Once the RIE sends `POST /status/{id}/ready`, the mock automatically fires one invocation with `{"counter": 0}` and logs the result.

## Trigger endpoints

Two helper endpoints let you fire additional invocations manually after startup:

| Endpoint | Payload |
|----------|---------|
| `GET /test` | `{"counter": 0}` — expects a successful response |
| `GET /fail` | `{"counter": 0, "fail": "yes"}` — expects an error response |

```bash
curl http://localhost:48490/test
curl http://localhost:48490/fail
```

All RIE callbacks (`/invocations/*/response`, `/invocations/*/error`, `/invocations/*/logs`, `/status/*/*`) are logged to stdout and return `202 Accepted`.
