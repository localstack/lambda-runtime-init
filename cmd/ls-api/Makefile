THIS_MAKEFILE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT         := $(abspath $(THIS_MAKEFILE_DIR)/../..)

ARCH         ?= x86_64
MOCK_PORT    := 48490
INTEROP_PORT := 9563
RIE_BINARY   := $(REPO_ROOT)/bin/aws-lambda-rie-$(ARCH)

.PHONY: build-rie start-mock start-rie

build-rie:  ## Build the RIE Linux binary via Go cross-compilation (works on macOS)
	$(MAKE) -C $(REPO_ROOT) ARCH=$(ARCH) compile-lambda-linux

start-mock:  ## Run the ls-api LocalStack endpoint mock natively (no Docker needed)
	go run $(THIS_MAKEFILE_DIR)

start-rie: build-rie  ## Build and run the RIE inside a Docker Python Lambda container
	docker run --rm \
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
		--entrypoint /var/rapid/init \
		public.ecr.aws/lambda/python:3.12
