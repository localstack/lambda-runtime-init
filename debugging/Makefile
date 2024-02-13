# Golang EOL overview: https://endoflife.date/go
DOCKER_GOLANG_IMAGE ?= golang:1.20-bullseye

# On ARM hosts, use: make ARCH=arm64 build-init
# Check host architecture: uname -m
# x86_64 or arm64
ARCH ?= x86_64

ifeq ($(ARCH), arm64)
 GOARCH=arm64
else
 GOARCH=amd64
endif

# Limitation: Debugging x86_64 lambdas does not work on ARM machines due to missing ptrace implementation in qemu used by Docker
# The function aborts with "could not launch process: fork/exec /var/rapid/init: function not implemented"
# * Discussion: https://github.com/aws/aws-sam-cli/discussions/4706
# * Docker for Mac: https://github.com/docker/for-mac/issues/5191#issuecomment-834154431


all: build

# build & "package" necessary files for debugging
build build-init: build-rapid build-delve/dlv
	cp ../bin/aws-lambda-rie-$(ARCH) ./init/var/rapid/init
	cp ./build-delve/dlv ./init/var/rapid/dlv

build-rapid:
	cd .. && GC_FLAGS="all=-N -l" make ARCH=$(ARCH) compile-lambda-linux

build-delve/dlv:
	docker run --rm -v $$(pwd)/build-delve/:/app/ $(DOCKER_GOLANG_IMAGE) \
		bash -c "cd /app && export GOARCH=$(GOARCH) && ./build.sh"

clean clean-init: clean-rapid clean-delve

clean-rapid:
	rm -rf ../bin/aws-lambda-rie-*
	rm -rf ./init/var/rapid/init

clean-delve:
	rm -rf ./build-delve/dlv
	rm -rf ./init/var/rapid/dlv

.PHONY: build build-rapid clean clean-rapid clean-delve
