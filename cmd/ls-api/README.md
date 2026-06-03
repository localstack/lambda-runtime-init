# ls-api — LocalStack endpoint mock

A lightweight HTTP server that stands in for the LocalStack endpoint when testing the RIE in isolation, without a running LocalStack instance.

## Ports

| Port | Direction | Purpose |
|------|-----------|---------|
| `48490` | inbound | Receives callbacks from the RIE (logs, response, status) |
| `9563` | outbound | Sends invocations to the RIE's `/invoke` endpoint; must be **exposed** when the RIE runs in Docker |

## Prerequisites

- Go toolchain — to run the mock
- Docker Desktop — to run the RIE (the binary targets Linux)

## How to use

**Terminal 1 — start the mock:**

```bash
make start-mock
```

**Terminal 2 — build and start the RIE** pointing at the mock:

```bash
make start-rie
```

This cross-compiles the RIE for Linux and runs it inside a `public.ecr.aws/lambda/python:3.12` container using `handler.py` as the Lambda function. Port `9563` is exposed so the mock can deliver invocations. Once the RIE sends `POST /status/{id}/ready`, the mock automatically fires one invocation with `{"counter": 0}` and logs the result.

To build for ARM (e.g. Apple Silicon):

```bash
make start-rie ARCH=arm64
```

## Trigger endpoints

Two helper endpoints let you fire additional invocations manually after startup:

| Endpoint | Payload |
|----------|---------|
| `GET /success` | `{"counter": 0}` — expects a successful response |
| `GET /fail` | `{"counter": 0, "fail": "yes"}` — expects an error response |

```bash
make success
make fail
```

All RIE callbacks (`/invocations/*/response`, `/invocations/*/error`, `/invocations/*/logs`, `/status/*/*`) are logged to stdout and return `202 Accepted`.

## Automated smoke test

To run the full e2e smoke test non-interactively (used in CI):

```bash
make smoke-test
```

This builds both the RIE binary and the ls-api mock, starts them, verifies a successful and a failing invocation, then cleans up.
