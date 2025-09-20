SHELL := /bin/bash

GOOS ?= linux
GOARCH ?= arm64
CGO_ENABLED ?= 0

DIST := dist
EMAIL_INGEST_ZIP := $(DIST)/email_ingest.zip
LOSEIT_TRANSFORM_ZIP := $(DIST)/loseit_transform.zip
WEEKLY_REPORT_ZIP := $(DIST)/weekly_report.zip

.PHONY: all build-all tidy tidy-lambdas lambda-email lambda-transform lambda-weekly test test-coverage infra-preview infra-up clean

all: build-all

build-all: tidy lambda-email lambda-transform lambda-weekly

tidy:
	go mod tidy
	cd infra && go mod tidy

tidy-lambdas:
	cd lambda/email_ingest && go mod tidy
	cd lambda/loseit_transform && go mod tidy
	cd lambda/weekly_report && go mod tidy

lambda-email:
	./scripts/build-lambda.sh email_ingest $(EMAIL_INGEST_ZIP)

lambda-transform:
	./scripts/build-lambda.sh loseit_transform $(LOSEIT_TRANSFORM_ZIP)

lambda-weekly:
	./scripts/build-lambda.sh weekly_report $(WEEKLY_REPORT_ZIP)

test:
	$(MAKE) tidy-lambdas
	@echo "Running tests for email_ingest..."
	cd lambda/email_ingest && go test -v -race ./...
	@echo "Running tests for loseit_transform..."
	cd lambda/loseit_transform && go test -v -race ./...
	@echo "Running tests for weekly_report..."
	cd lambda/weekly_report && go test -v -race ./...
	@echo "✅ All tests passed!"

test-coverage:
	$(MAKE) tidy-lambdas
	@echo "Running tests with coverage..."
	@mkdir -p $(DIST)/coverage
	cd lambda/email_ingest && go test -race -coverprofile=../../$(DIST)/coverage/email_ingest.out ./...
	cd lambda/loseit_transform && go test -race -coverprofile=../../$(DIST)/coverage/loseit_transform.out ./...
	cd lambda/weekly_report && go test -race -coverprofile=../../$(DIST)/coverage/weekly_report.out ./...
	@echo "Coverage reports generated in $(DIST)/coverage/"

infra-preview:
	cd infra && pulumi preview

infra-up:
	cd infra && pulumi up --yes

clean:
	rm -rf $(DIST)
	find . -name "*.test" -delete
