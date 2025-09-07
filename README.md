# Mailmunch AWS + Pulumi + Go starter

This repo is a minimal scaffold for an AWS project using Pulumi (Go) and Go-based AWS Lambdas. It provisions:

- S3 bucket (artifacts)
- ECR repository
- Secrets Manager secret
- AppConfig application/profile/version (hosted)
- Lambda functions for email processing and data transformation

And includes:

- GitHub Actions (CI, Pulumi preview, and manual Pulumi up)
- Go linting via golangci-lint
- Comprehensive test suite with coverage reporting
- Makefile and build script to package Lambda

## Prerequisites

- Go 1.22+
- Pulumi CLI
- AWS credentials configured (locally or via OIDC in CI)

## Layout

- `lambda/hello`: Go Lambda "hello world" handler
- `lambda/email_ingest`: Email processing Lambda for extracting CSV attachments
- `lambda/loseit_transform`: Data transformation Lambda for converting CSV to Parquet
- `infra`: Pulumi Go program
- `.github/workflows`: CI/CD workflows
- `scripts/build-lambda.sh`: builds a Linux/arm64 binary and zips it

## LoseIt Data Pipeline

This project includes a complete serverless data processing pipeline for LoseIt food diary exports:

```text
SES → S3 (raw emails) → Lambda (extract CSV) → S3 (raw CSV) → Lambda (transform) → S3 (Parquet) → Athena
```

### S3 Structure

```text
s3://mailmunch-data/
  raw/
    email/year=2025/month=08/day=27/<message-id>.eml
    loseit_csv/year=2025/month=08/day=27/loseit-daily.csv
  curated/
    loseit_parquet/year=2025/month=08/day=27/part-0000.snappy.parquet
```

## Quick start

### Development

1. Run tests

```bash
make test
```

1. Run tests with coverage

```bash
make test-coverage
```

1. Build lambda zips

```bash
make lambda          # builds hello lambda
make lambda-email    # builds email_ingest lambda  
make lambda-transform # builds loseit_transform lambda
```

### Deployment

1. Build lambda zip

```bash
make lambda
```

1. Configure Pulumi stack

```bash
cd infra
pulumi stack init dev || true
pulumi config set mailmunch:region us-east-1
```

1. Preview infra

```bash
make infra-preview
```

1. Deploy

```bash
make infra-up
```

## CI/CD secrets

Set GitHub secrets if using OIDC deploys:

- `AWS_ROLE_TO_ASSUME`
- `AWS_REGION` (e.g., us-east-1)
- `PULUMI_ACCESS_TOKEN`

## Notes

- The hello Lambda uses custom runtime `provided.al2`. The build script produces a `bootstrap` binary in the zip.
- SES is referenced implicitly (no resources created by default) to avoid requiring domain verification upfront. Add identities/routes later as needed.
