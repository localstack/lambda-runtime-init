# TODO: build arm-compatible

build-rapid:
	cd .. && GC_FLAGS="all=-N -l" make compile-lambda-linux

build-delve/dlv:
	docker run --rm -v $$(pwd)/build-delve/:/app/ golang:1.18.2 bash -c "cd /app && ./build.sh"

# build & "package" necessary files for debugging
build-init: build-rapid build-delve/dlv
	cp ../bin/aws-lambda-rie-* ./init/var/rapid/init
	cp ./build-delve/dlv ./init/var/rapid/dlv

.PHONY: build-init build-rapid