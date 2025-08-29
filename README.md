# Mailmunch AWS + Pulumi + Go starter

This repo is a minimal scaffold for an AWS project using Pulumi (Go) and Go-based AWS Lambdas. It provisions:

- S3 bucket (artifacts)
- ECR repository
- Secrets Manager secret
- AppConfig application/profile/version (hosted)
- A hello Lambda (custom runtime provided.al2, arm64)

And includes:

- GitHub Actions (CI, Pulumi preview, and manual Pulumi up)
- Go linting via golangci-lint
- Makefile and build script to package Lambda

## Prerequisites

- Go 1.24+
- Pulumi CLI
- AWS credentials configured (locally or via OIDC in CI)

## Layout

- `lambda/hello`: Go Lambda "hello world" handler
- `infra`: Pulumi Go program
- `.github/workflows`: CI/CD workflows
- `scripts/build-lambda.sh`: builds a Linux/arm64 binary and zips it

## Quick start

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
