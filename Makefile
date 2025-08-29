SHELL := /bin/bash

GOOS ?= linux
GOARCH ?= arm64
CGO_ENABLED ?= 0

LAMBDA_NAME := hello
DIST := dist
LAMBDA_ZIP := $(DIST)/hello.zip

.PHONY: all build-all tidy lambda infra-preview infra-up clean

all: build-all

build-all: tidy lambda

tidy:
	go mod tidy
	cd infra && go mod tidy

lambda:
	./scripts/build-lambda.sh $(LAMBDA_NAME) $(LAMBDA_ZIP)

infra-preview:
	cd infra && pulumi preview

infra-up:
	cd infra && pulumi up --yes

clean:
	rm -rf $(DIST)
	find . -name "*.test" -delete
