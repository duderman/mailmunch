# Mailmunch AWS + Pulumi + Go starter

This repo is a minimal scaffold for an AWS project using Pulumi (Go) and Go-based AWS Lambdas. It provisions:

- S3 buckets (artifacts + data with lifecycle policies)
- ECR repository
- Secrets Manager secret
- AppConfig application/profile/version (hosted)
- SES email receiving with dedicated recipient filtering
- Lambda functions for email processing and data transformation
- Glue database and crawler for analytics

And includes:

- GitHub Actions (CI, Pulumi preview, and manual Pulumi up)
- Go linting via golangci-lint
- Comprehensive test suite with coverage reporting
- Makefile and build script to package Lambda

## Prerequisites

- Go 1.22+
- Pulumi CLI
- AWS credentials configured (locally or via OIDC in CI)
- SES domain verification (for email receiving)

## Layout

- `lambda/email_ingest`: Email processing Lambda for LoseIt domain filtering and CSV extraction
- `lambda/loseit_transform`: Data transformation Lambda for converting CSV to Parquet
- `infra`: Pulumi Go program
- `.github/workflows`: CI/CD workflows
- `scripts/build-lambda.sh`: builds a Linux/arm64 binary and zips it

## LoseIt Data Pipeline

This project includes a complete serverless data processing pipeline for LoseIt food diary exports:

```text
SES (recipient filter) → S3 incoming/ (90-day retention) → Lambda (LoseIt check) → S3 analytics/ (forever) + CSV → Lambda → Parquet → Athena
```

### Email Processing Flow

1. **SES receives emails** - only for configured recipient address
2. **All emails saved** to `raw/email/incoming/` with 90-day retention
3. **S3 triggers Lambda** for each incoming email
4. **Lambda checks if LoseIt email**:
   - **If YES**: Saves to analytics path + extracts CSV attachments
   - **If NO**: Ignores (stays in incoming/ with retention)
5. **CSV triggers transform** Lambda to create Parquet files
6. **Glue crawler** makes data queryable in Athena

### Weekly Report System

The system includes an AI-powered weekly nutrition analysis with secure credential management:

1. **EventBridge Scheduler** triggers weekly report Lambda every Sunday at 6 PM London time
2. **Weekly Report Lambda** queries the past week's food data from S3
3. **OpenAI API integration** analyzes nutrition data and provides personalized recommendations (API key securely stored in AWS Secrets Manager)
4. **SES email delivery** sends comprehensive HTML and text reports
5. **AI analysis includes**:
   - Week-over-week nutrition comparison
   - Weight loss recommendations with specific food swaps
   - Muscle growth nutrition guidance and protein timing
   - Food quality analysis (whole vs processed foods)
   - Actionable 5-step plan for the upcoming week

### S3 Structure

```text
s3://mailmunch-data/
  raw/
    email/
      incoming/           # All emails (90-day retention)
      year=2025/month=08/day=27/<message-id>.eml  # LoseIt analytics (forever)
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
make lambda-email    # builds email_ingest lambda
make lambda-transform # builds loseit_transform lambda
make lambda-weekly    # builds weekly_report lambda
```

### Deployment

1. Build lambda zips

```bash
make build-all
```

1. Configure Pulumi stack

```bash
cd infra
pulumi stack init dev || true
pulumi config set mailmunch:region us-east-1
pulumi config set mailmunch:dataBucketName mailmunch-data            # optional: S3 bucket name
pulumi config set mailmunch:allowedSenderDomain loseit.com           # optional: allowed email domain
pulumi config set mailmunch:sesEmailIdentity mailmunch.co.uk         # optional: SES email identity
pulumi config set mailmunch:recipientAddress reports@mailmunch.co.uk # required: recipient address
pulumi config set mailmunch:openaiApiKey "sk-..."                    # required: OpenAI API key for weekly reports (stored in Secrets Manager)
pulumi config set mailmunch:reportEmail reports@mailmunch.co.uk      # required: email for weekly reports
pulumi config set mailmunch:senderEmail reports@mailmunch.co.uk      # required: sender email for reports
```

1. Preview infra

```bash
make infra-preview
```

1. Deploy

```bash
make infra-up
```

### Configuration Options

- `mailmunch:dataBucketName` - S3 bucket name for data storage (default: "mailmunch-data")
- `mailmunch:allowedSenderDomain` - Domain to filter emails from (default: "loseit.com")
- `mailmunch:sesEmailIdentity` - SES email identity for domain verification (optional)
- `mailmunch:recipientAddress` - Email address that SES will process (required for email receiving)
- `mailmunch:openaiApiKey` - OpenAI API key for AI-powered weekly analysis (securely stored in AWS Secrets Manager)
- `mailmunch:reportEmail` - Email address to receive weekly nutrition reports (required for weekly reports)
- `mailmunch:senderEmail` - Email address to send reports from (required for weekly reports, must be verified in SES)

## CI/CD secrets

Set GitHub secrets if using OIDC deploys:

- `AWS_ROLE_TO_ASSUME`
- `AWS_REGION` (e.g., us-east-1)
- `PULUMI_ACCESS_TOKEN`

## Notes

- All Lambda functions use custom runtime `provided.al2`. The build script produces a `bootstrap` binary in the zip.
- SES email receiving requires domain verification and MX record configuration
- Email processing includes intelligent filtering - only LoseIt emails are processed for analytics
- Non-LoseIt emails are retained for 90 days in the incoming folder, then automatically deleted
- Processed LoseIt data is kept forever for analytics and machine learning
- Glue crawler automatically discovers schema changes in Parquet files for Athena queries
