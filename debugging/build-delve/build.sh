#!/usr/bin/env bash

mkdir -p /tmp/build-delve
cd /tmp/build-delve
git clone https://github.com/go-delve/delve.git
cd delve
make build
mv dlv /app/dlv