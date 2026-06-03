THIS_MAKEFILE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT         := $(abspath $(THIS_MAKEFILE_DIR)/../..)

ARCH         ?= x86_64
MOCK_PORT    := 48490
INTEROP_PORT := 9563
RIE_BINARY   := $(REPO_ROOT)/bin/aws-lambda-rie-$(ARCH)
LS_API_BIN   := $(REPO_ROOT)/bin/ls-api

# Common docker flags for start-rie and start-rie-detached.
# Uses deferred assignment (=) so $$LATEST is not expanded until recipe time,
# where Make reduces $$ -> $ before passing the line to the shell.
RIE_DOCKER_OPTS = \
	--platform linux/amd64 \
	--add-host=host.docker.internal:host-gateway \
	-p $(INTEROP_PORT):$(INTEROP_PORT) \
	-v $(RIE_BINARY):/var/rapid/init:ro \
	-v $(THIS_MAKEFILE_DIR)/handler.py:/var/task/handler.py:ro \
	-e LOCALSTACK_RUNTIME_ENDPOINT=http://host.docker.internal:$(MOCK_PORT) \
	-e LOCALSTACK_RUNTIME_ID=test-runtime-id \
	-e AWS_LAMBDA_FUNCTION_TIMEOUT=30 \
	-e AWS_LAMBDA_FUNCTION_VERSION='$$LATEST' \
	-e AWS_LAMBDA_FUNCTION_MEMORY_SIZE=128 \
	-e AWS_REGION=us-east-1 \
	-e _HANDLER=handler.handler \
	--entrypoint /var/rapid/init

.PHONY: build-rie build-ls-api start-mock start-rie start-rie-detached success fail smoke-test

build-rie:  ## Build the RIE Linux binary via Go cross-compilation (works on macOS)
	$(MAKE) -C $(REPO_ROOT) ARCH=$(ARCH) compile-lambda-linux

build-ls-api:  ## Build the ls-api mock binary
	go build -o $(LS_API_BIN) $(THIS_MAKEFILE_DIR)

start-mock:  ## Run the ls-api LocalStack endpoint mock natively (no Docker needed)
	go run $(THIS_MAKEFILE_DIR)

start-rie: build-rie  ## Build and run the RIE inside a Docker Python Lambda container
	docker run --rm $(RIE_DOCKER_OPTS) public.ecr.aws/lambda/python:3.12

start-rie-detached:  ## Start the RIE in detached mode; prints container ID (binaries must be pre-built)
	@docker run --detach $(RIE_DOCKER_OPTS) public.ecr.aws/lambda/python:3.12

success:  ## Trigger a successful invocation via the mock's /success endpoint
	curl -sf http://localhost:$(MOCK_PORT)/success

fail:  ## Trigger an error invocation via the mock's /fail endpoint
	curl -sf http://localhost:$(MOCK_PORT)/fail

smoke-test: build-rie build-ls-api  ## Full e2e smoke test: start mock + RIE, verify success + error invocations, cleanup
	$(THIS_MAKEFILE_DIR)/smoke-test.sh
