package main

import (
	"fmt"
	"os"

	aws "github.com/pulumi/pulumi-aws/sdk/v6/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/appconfig"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ecr"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/glue"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/secretsmanager"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ses"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/sesv2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		project := ctx.Project()
		stack := ctx.Stack()

		// Create a single AWS provider with default tags applied to all supported resources.
		prov, err := aws.NewProvider(ctx, "prov", &aws.ProviderArgs{
			DefaultTags: &aws.ProviderDefaultTagsArgs{
				Tags: pulumi.StringMap{
					"Project":   pulumi.String(project),
					"Stack":     pulumi.String(stack),
					"ManagedBy": pulumi.String("Pulumi"),
				},
			},
		})
		if err != nil {
			return err
		}
		awsOpts := pulumi.Provider(prov)

		bucket, err := s3.NewBucket(ctx, fmt.Sprintf("%s-%s-artifacts", project, stack), &s3.BucketArgs{}, awsOpts)
		if err != nil {
			return err
		}
		_, err = s3.NewBucketPublicAccessBlock(ctx, fmt.Sprintf("%s-%s-artifacts-pab", project, stack), &s3.BucketPublicAccessBlockArgs{
			Bucket:                bucket.ID(),
			BlockPublicAcls:       pulumi.Bool(true),
			BlockPublicPolicy:     pulumi.Bool(true),
			IgnorePublicAcls:      pulumi.Bool(true),
			RestrictPublicBuckets: pulumi.Bool(true),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Data bucket for raw and curated layers (configurable; default "mailmunch-data")
		dataBucketName := "mailmunch-data"
		if v, ok := ctx.GetConfig("mailmunch:dataBucketName"); ok && v != "" {
			dataBucketName = v
		}

		// Allowed sender domain for email filtering (configurable; default "loseit.com")
		allowedSenderDomain := "loseit.com"
		if v, ok := ctx.GetConfig("mailmunch:allowedSenderDomain"); ok && v != "" {
			allowedSenderDomain = v
		}

		// Data catalog settings for Athena queries.
		athenaDatabaseName := fmt.Sprintf("%s_%s", project, stack)
		if v, ok := ctx.GetConfig("mailmunch:athenaDatabaseName"); ok && v != "" {
			athenaDatabaseName = v
		}

		athenaTableName := "loseit_entries"
		if v, ok := ctx.GetConfig("mailmunch:athenaTableName"); ok && v != "" {
			athenaTableName = v
		}

		emailsBucket, err := s3.NewBucket(ctx, dataBucketName, &s3.BucketArgs{
			Bucket: pulumi.String(dataBucketName),
		}, awsOpts)
		if err != nil {
			return err
		}
		_, err = s3.NewBucketPublicAccessBlock(ctx, fmt.Sprintf("%s-%s-emails-pab", project, stack), &s3.BucketPublicAccessBlockArgs{
			Bucket:                emailsBucket.ID(),
			BlockPublicAcls:       pulumi.Bool(true),
			BlockPublicPolicy:     pulumi.Bool(true),
			IgnorePublicAcls:      pulumi.Bool(true),
			RestrictPublicBuckets: pulumi.Bool(true),
		}, awsOpts)
		if err != nil {
			return err
		}

		// S3 lifecycle rules for email retention
		_, err = s3.NewBucketLifecycleConfigurationV2(ctx, fmt.Sprintf("%s-%s-emails-lifecycle", project, stack), &s3.BucketLifecycleConfigurationV2Args{
			Bucket: emailsBucket.ID(),
			Rules: s3.BucketLifecycleConfigurationV2RuleArray{
				&s3.BucketLifecycleConfigurationV2RuleArgs{
					Id:     pulumi.String("expire-raw-incoming-emails"),
					Status: pulumi.String("Enabled"),
					Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{
						Prefix: pulumi.String("raw/email/incoming/"),
					},
					Expiration: &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{
						Days: pulumi.Int(90), // Expire raw incoming emails after 90 days
					},
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		repo, err := ecr.NewRepository(ctx, fmt.Sprintf("%s-%s-repo", project, stack), &ecr.RepositoryArgs{
			ImageScanningConfiguration: &ecr.RepositoryImageScanningConfigurationArgs{
				ScanOnPush: pulumi.Bool(true),
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		secret, err := secretsmanager.NewSecret(ctx, fmt.Sprintf("%s-%s-secret", project, stack), &secretsmanager.SecretArgs{}, awsOpts)
		if err != nil {
			return err
		}

		app, err := appconfig.NewApplication(ctx, fmt.Sprintf("%s-%s-appcfg", project, stack), &appconfig.ApplicationArgs{}, awsOpts)
		if err != nil {
			return err
		}
		profile, err := appconfig.NewConfigurationProfile(ctx, fmt.Sprintf("%s-%s-profile", project, stack), &appconfig.ConfigurationProfileArgs{
			ApplicationId: app.ID(),
			LocationUri:   pulumi.String("hosted"),
		}, awsOpts)
		if err != nil {
			return err
		}
		// Read the prompt from the text file
		promptContent, err := os.ReadFile("weekly_report_prompt.txt")
		if err != nil {
			return fmt.Errorf("failed to read weekly_report_prompt.txt: %w", err)
		}

		// Create JSON configuration with the prompt
		configJSON := fmt.Sprintf(`{
			"weekly_report_base_prompt": %q
		}`, string(promptContent))

		configVersion, err := appconfig.NewHostedConfigurationVersion(ctx, fmt.Sprintf("%s-%s-configv1", project, stack), &appconfig.HostedConfigurationVersionArgs{
			ApplicationId:          app.ID(),
			ConfigurationProfileId: profile.ConfigurationProfileId,
			Content:                pulumi.String(configJSON),
			ContentType:            pulumi.String("application/json"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create AppConfig environment
		env, err := appconfig.NewEnvironment(ctx, fmt.Sprintf("%s-%s-env-prod", project, stack), &appconfig.EnvironmentArgs{
			Name:          pulumi.String("prod"),
			ApplicationId: app.ID(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create AppConfig deployment to make the configuration available
		_, err = appconfig.NewDeployment(ctx, fmt.Sprintf("%s-%s-deployment", project, stack), &appconfig.DeploymentArgs{
			ApplicationId:          app.ID(),
			ConfigurationProfileId: profile.ConfigurationProfileId,
			ConfigurationVersion:   pulumi.Sprintf("%d", configVersion.VersionNumber),
			EnvironmentId:          env.EnvironmentId,
			DeploymentStrategyId:   pulumi.String("AppConfig.AllAtOnce"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Lambda assume role policy
		lambdaAssumeRolePolicy, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
			Statements: []iam.GetPolicyDocumentStatement{
				{
					Effect: pulumi.StringRef("Allow"),
					Principals: []iam.GetPolicyDocumentStatementPrincipal{
						{
							Type: "Service",
							Identifiers: []string{
								"lambda.amazonaws.com",
							},
						},
					},
					Actions: []string{
						"sts:AssumeRole",
					},
				},
			},
		}, nil)
		if err != nil {
			return err
		}

		// Optionally create SES email identity if configured
		if email, ok := ctx.GetConfig("mailmunch:sesEmailIdentity"); ok && email != "" {
			sesOpts := []pulumi.ResourceOption{awsOpts, pulumi.Import(pulumi.ID(email))}
			_, err = sesv2.NewEmailIdentity(ctx, fmt.Sprintf("%s-%s-ses-identity", project, stack), &sesv2.EmailIdentityArgs{
				EmailIdentity: pulumi.String(email),
			}, sesOpts...)
			if err != nil {
				return err
			}
		}

		// Lambda that parses incoming EML and writes raw + CSV to partitioned prefixes
		ingestRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-email-ingest-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(lambdaAssumeRolePolicy.Json),
		}, awsOpts)
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-%s-email-ingest-basic", project, stack), &iam.RolePolicyAttachmentArgs{
			Role:      ingestRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create S3 access policy for email ingest Lambda
		s3PolicyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
			Statements: []iam.GetPolicyDocumentStatement{
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:GetObject",
						"s3:PutObject",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName + "/*"},
				},
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:ListBucket",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName},
				},
			},
		}, nil)
		if err != nil {
			return err
		}

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-email-ingest-s3", project, stack), &iam.RolePolicyArgs{
			Role:   ingestRole.ID(),
			Policy: pulumi.String(s3PolicyDoc.Json),
		}, awsOpts)
		if err != nil {
			return err
		}

		emailIngestZip := pulumi.NewFileArchive("../dist/email_ingest.zip")
		emailIngestFn, err := lambda.NewFunction(ctx, fmt.Sprintf("%s-%s-email-ingest", project, stack), &lambda.FunctionArgs{
			Role:          ingestRole.Arn,
			Runtime:       pulumi.String("provided.al2"),
			Handler:       pulumi.String("bootstrap"),
			Architectures: pulumi.ToStringArray([]string{"arm64"}),
			Code:          emailIngestZip,
			Environment: &lambda.FunctionEnvironmentArgs{
				Variables: pulumi.StringMap{
					"EMAIL_BUCKET":          emailsBucket.Bucket,
					"INCOMING_PREFIX":       pulumi.String("raw/email/incoming/"),
					"RAW_EMAIL_BASE":        pulumi.String("raw/email/"),
					"RAW_CSV_BASE":          pulumi.String("raw/loseit_csv/"),
					"ALLOWED_SENDER_DOMAIN": pulumi.String(allowedSenderDomain),
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		// Allow S3 to invoke the email ingest Lambda
		_, err = lambda.NewPermission(ctx, fmt.Sprintf("%s-%s-email-ingest-perm", project, stack), &lambda.PermissionArgs{
			Action:    pulumi.String("lambda:InvokeFunction"),
			Function:  emailIngestFn.Name,
			Principal: pulumi.String("s3.amazonaws.com"),
			SourceArn: emailsBucket.Arn,
		}, awsOpts)
		if err != nil {
			return err
		}

		// S3 event notifications are configured later in a single resource

		// Permit SES to write to the emails bucket (for S3 action)
		caller := aws.GetCallerIdentityOutput(ctx, aws.GetCallerIdentityOutputArgs{})
		_, err = s3.NewBucketPolicy(ctx, fmt.Sprintf("%s-%s-emails-policy", project, stack), &s3.BucketPolicyArgs{
			Bucket: emailsBucket.ID(),
			Policy: pulumi.All(emailsBucket.Arn, caller.AccountId()).ApplyT(func(vals []interface{}) string {
				arn := vals[0].(string)
				acct := vals[1].(string)
				// Use a static policy template to avoid gRPC issues
				policyJson := fmt.Sprintf(`{
					"Version": "2008-10-17",
					"Statement": [
						{
							"Sid": "AllowSESPuts",
							"Effect": "Allow",
							"Principal": {
								"Service": "ses.amazonaws.com"
							},
							"Action": "s3:PutObject",
							"Resource": "%s/*",
							"Condition": {
								"StringEquals": {
									"aws:Referer": "%s"
								}
							}
						}
					]
				}`, arn, acct)
				return policyJson
			}).(pulumi.StringOutput),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Weekly Report Lambda Function
		weeklyReportRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-weekly-report-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2012-10-17",
				"Statement": [
					{
						"Effect": "Allow",
						"Principal": {
							"Service": "lambda.amazonaws.com"
						},
						"Action": "sts:AssumeRole"
					}
				]
			}`),
		}, awsOpts)
		if err != nil {
			return err
		}

		_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-%s-weekly-report-basic", project, stack), &iam.RolePolicyAttachmentArgs{
			Role:      weeklyReportRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// SES policy for weekly report Lambda to send emails
		weeklyReportSESPolicyDoc := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			Statements: iam.GetPolicyDocumentStatementArray{
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"ses:SendEmail",
						"ses:SendRawEmail",
					}),
					Resources: pulumi.StringArray{
						pulumi.String("*"),
					},
				},
			},
		})

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-weekly-report-ses", project, stack), &iam.RolePolicyArgs{
			Role:   weeklyReportRole.ID(),
			Policy: weeklyReportSESPolicyDoc.Json(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create OpenAI API key secret
		openaiSecret, err := secretsmanager.NewSecret(ctx, fmt.Sprintf("%s-%s-openai-secret", project, stack), &secretsmanager.SecretArgs{
			Description: pulumi.String("OpenAI API key for weekly nutrition reports"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Get OpenAI API key from config and store in Secrets Manager
		openaiApiKey := ""
		if v, ok := ctx.GetConfig("mailmunch:openaiApiKey"); ok {
			openaiApiKey = v
		}

		// Only create secret version if API key is provided
		if openaiApiKey != "" {
			_, err = secretsmanager.NewSecretVersion(ctx, fmt.Sprintf("%s-%s-openai-secret-version", project, stack), &secretsmanager.SecretVersionArgs{
				SecretId:     openaiSecret.ID(),
				SecretString: pulumi.String(openaiApiKey),
			}, awsOpts)
			if err != nil {
				return err
			}
		}

		// Add Secrets Manager policy for weekly report Lambda
		weeklyReportSecretsPolicy := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			Statements: iam.GetPolicyDocumentStatementArray{
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"secretsmanager:GetSecretValue",
					}),
					Resources: pulumi.StringArray{
						openaiSecret.Arn,
					},
				},
			},
		})

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-weekly-report-secrets", project, stack), &iam.RolePolicyArgs{
			Role:   weeklyReportRole.ID(),
			Policy: weeklyReportSecretsPolicy.Json(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Add Athena policy for weekly report Lambda
		weeklyReportAthenaPolicy := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			Statements: iam.GetPolicyDocumentStatementArray{
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"athena:StartQueryExecution",
						"athena:GetQueryExecution",
						"athena:GetQueryResults",
						"athena:StopQueryExecution",
						"glue:GetDatabase",
						"glue:GetTable",
						"glue:GetPartitions",
					}),
					Resources: pulumi.ToStringArray([]string{
						"*", // Athena and Glue resources don't support fine-grained ARNs
					}),
				},
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"s3:GetBucketLocation",
						"s3:GetObject",
						"s3:ListBucket",
						"s3:PutObject",
						"s3:DeleteObject",
					}),
					Resources: pulumi.StringArray{
						emailsBucket.Arn,
						pulumi.Sprintf("%s/*", emailsBucket.Arn),
					},
				},
			},
		})

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-weekly-report-athena", project, stack), &iam.RolePolicyArgs{
			Role:   weeklyReportRole.ID(),
			Policy: weeklyReportAthenaPolicy.Json(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Add AppConfig policy for weekly report Lambda
		weeklyReportAppConfigPolicy := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			Statements: iam.GetPolicyDocumentStatementArray{
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"appconfig:GetConfiguration",
						"appconfig:GetLatestConfiguration",
						"appconfig:StartConfigurationSession",
					}),
					Resources: pulumi.ToStringArray([]string{
						"*", // AppConfig permissions require broad access
					}),
				},
			},
		})

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-weekly-report-appconfig", project, stack), &iam.RolePolicyArgs{
			Role:   weeklyReportRole.ID(),
			Policy: weeklyReportAppConfigPolicy.Json(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Get email configuration
		reportEmail := ""
		if v, ok := ctx.GetConfig("mailmunch:reportEmail"); ok {
			reportEmail = v
		}

		senderEmail := ""
		if v, ok := ctx.GetConfig("mailmunch:senderEmail"); ok {
			senderEmail = v
		}

		weeklyReportZip := pulumi.NewFileArchive("../dist/weekly_report.zip")
		weeklyReportFn, err := lambda.NewFunction(ctx, fmt.Sprintf("%s-%s-weekly-report", project, stack), &lambda.FunctionArgs{
			Role:          weeklyReportRole.Arn,
			Runtime:       pulumi.String("provided.al2"),
			Handler:       pulumi.String("bootstrap"),
			Architectures: pulumi.ToStringArray([]string{"arm64"}),
			Code:          weeklyReportZip,
			Timeout:       pulumi.Int(300), // 5 minutes for OpenAI API calls
			Environment: &lambda.FunctionEnvironmentArgs{
				Variables: pulumi.StringMap{
					"OPENAI_SECRET_ARN":       openaiSecret.Arn,
					"REPORT_EMAIL":            pulumi.String(reportEmail),
					"SENDER_EMAIL":            pulumi.String(senderEmail),
					"ATHENA_DATABASE":         pulumi.String(athenaDatabaseName),
					"ATHENA_TABLE":            pulumi.String(athenaTableName),
					"ATHENA_WORKGROUP":        pulumi.String("primary"),
					"ATHENA_RESULTS_BUCKET":   emailsBucket.Bucket,
					"APPCONFIG_APPLICATION":   app.ID(),
					"APPCONFIG_ENVIRONMENT":   pulumi.String("prod"),
					"APPCONFIG_CONFIGURATION": profile.ConfigurationProfileId,
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		// EventBridge Scheduler role
		schedulerRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-scheduler-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2012-10-17",
				"Statement": [
					{
						"Effect": "Allow",
						"Principal": {
							"Service": "scheduler.amazonaws.com"
						},
						"Action": "sts:AssumeRole"
					}
				]
			}`),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Lambda invoke policy for scheduler
		schedulerLambdaPolicyDoc := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			Statements: iam.GetPolicyDocumentStatementArray{
				iam.GetPolicyDocumentStatementArgs{
					Effect: pulumi.String("Allow"),
					Actions: pulumi.ToStringArray([]string{
						"lambda:InvokeFunction",
					}),
					Resources: pulumi.StringArray{
						weeklyReportFn.Arn,
					},
				},
			},
		})

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-scheduler-lambda", project, stack), &iam.RolePolicyArgs{
			Role:   schedulerRole.ID(),
			Policy: schedulerLambdaPolicyDoc.Json(),
		}, awsOpts)
		if err != nil {
			return err
		}

		// EventBridge Scheduler - every Sunday at 6 PM London time
		_, err = scheduler.NewSchedule(ctx, fmt.Sprintf("%s-%s-weekly-report-schedule", project, stack), &scheduler.ScheduleArgs{
			Description:        pulumi.String("Trigger weekly nutrition report every Sunday at 6 PM London time"),
			ScheduleExpression: pulumi.String("cron(0 18 ? * SUN *)"), // 6 PM UTC on Sundays (7 PM London time during DST, 6 PM during standard time)
			FlexibleTimeWindow: &scheduler.ScheduleFlexibleTimeWindowArgs{
				Mode: pulumi.String("OFF"),
			},
			Target: &scheduler.ScheduleTargetArgs{
				Arn:     weeklyReportFn.Arn,
				RoleArn: schedulerRole.Arn,
				Input:   pulumi.String(`{"source":"aws.scheduler","detail-type":"Weekly Report Trigger"}`),
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		ctx.Export("bucketName", bucket.Bucket)
		ctx.Export("dataBucket", emailsBucket.Bucket)
		ctx.Export("ecrRepositoryUrl", repo.RepositoryUrl)
		ctx.Export("secretArn", secret.Arn)
		ctx.Export("emailIngestLambda", emailIngestFn.Name)
		ctx.Export("weeklyReportLambda", weeklyReportFn.Name)
		ctx.Export("region", aws.GetRegionOutput(ctx, aws.GetRegionOutputArgs{}).Name())
		ctx.Export("allowedSenderDomain", pulumi.String(allowedSenderDomain))

		if v, ok := ctx.GetConfig("mailmunch:sesEmailIdentity"); ok {
			ctx.Export("sesEmailIdentity", pulumi.String(v))
		}

		// Glue database and crawler for curated Parquet
		glueDb, err := glue.NewCatalogDatabase(ctx, fmt.Sprintf("%s_%s_db", project, stack), &glue.CatalogDatabaseArgs{
			Name: pulumi.String(athenaDatabaseName),
		}, awsOpts)
		if err != nil {
			return err
		}

		loseitTableLocation := emailsBucket.Bucket.ApplyT(func(b string) string {
			return fmt.Sprintf("s3://%s/curated/loseit_parquet/", b)
		}).(pulumi.StringOutput)

		loseitTable, err := glue.NewCatalogTable(ctx, fmt.Sprintf("%s-%s-loseit-table", project, stack), &glue.CatalogTableArgs{
			DatabaseName: glueDb.Name,
			Name:         pulumi.String(athenaTableName),
			TableType:    pulumi.String("EXTERNAL_TABLE"),
			Parameters: pulumi.StringMap{
				"EXTERNAL":            pulumi.String("TRUE"),
				"classification":      pulumi.String("parquet"),
				"parquet.compression": pulumi.String("SNAPPY"),
			},
			StorageDescriptor: &glue.CatalogTableStorageDescriptorArgs{
				Location:     loseitTableLocation.ToStringPtrOutput(),
				InputFormat:  pulumi.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"),
				OutputFormat: pulumi.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"),
				SerDeInfo: &glue.CatalogTableStorageDescriptorSerDeInfoArgs{
					SerializationLibrary: pulumi.String("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"),
					Parameters: pulumi.StringMap{
						"serialization.format": pulumi.String("1"),
					},
				},
				Columns: glue.CatalogTableStorageDescriptorColumnArray{
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("record_type"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("date"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("meal"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("name"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("icon"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("quantity"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("units"), Type: pulumi.String("string")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("calories"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("deleted"), Type: pulumi.String("boolean")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("fat_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("protein_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("carbs_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("saturated_fat_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("sugar_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("fiber_g"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("cholesterol_mg"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("sodium_mg"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("duration_minutes"), Type: pulumi.String("double")},
					&glue.CatalogTableStorageDescriptorColumnArgs{Name: pulumi.String("distance_km"), Type: pulumi.String("double")},
				},
			},
			PartitionKeys: glue.CatalogTablePartitionKeyArray{
				&glue.CatalogTablePartitionKeyArgs{Name: pulumi.String("year"), Type: pulumi.String("string")},
				&glue.CatalogTablePartitionKeyArgs{Name: pulumi.String("month"), Type: pulumi.String("string")},
				&glue.CatalogTablePartitionKeyArgs{Name: pulumi.String("day"), Type: pulumi.String("string")},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		ctx.Export("loseitTable", loseitTable.Name)

		// Glue assume role policy
		glueAssumeRolePolicy, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
			Statements: []iam.GetPolicyDocumentStatement{
				{
					Effect: pulumi.StringRef("Allow"),
					Principals: []iam.GetPolicyDocumentStatementPrincipal{
						{
							Type: "Service",
							Identifiers: []string{
								"glue.amazonaws.com",
							},
						},
					},
					Actions: []string{
						"sts:AssumeRole",
					},
				},
			},
		}, nil)
		if err != nil {
			return err
		}

		glueRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-glue-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(glueAssumeRolePolicy.Json),
		}, awsOpts)
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-%s-glue-managed", project, stack), &iam.RolePolicyAttachmentArgs{
			Role:      glueRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSGlueServiceRole"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create S3 access policy for Glue (curated data access only)
		glueS3PolicyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
			Statements: []iam.GetPolicyDocumentStatement{
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:GetObject",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName + "/*"},
				},
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:ListBucket",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName},
					Conditions: []iam.GetPolicyDocumentStatementCondition{
						{
							Test:     "StringLike",
							Variable: "s3:prefix",
							Values:   []string{"curated/*"},
						},
					},
				},
			},
		}, nil)
		if err != nil {
			return err
		}

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-glue-s3", project, stack), &iam.RolePolicyArgs{
			Role:   glueRole.ID(),
			Policy: pulumi.String(glueS3PolicyDoc.Json),
		}, awsOpts)
		if err != nil {
			return err
		}

		_, err = glue.NewCrawler(ctx, fmt.Sprintf("%s-%s-loseit-crawler", project, stack), &glue.CrawlerArgs{
			DatabaseName: glueDb.Name,
			Role:         glueRole.Arn,
			S3Targets: glue.CrawlerS3TargetArray{
				&glue.CrawlerS3TargetArgs{Path: emailsBucket.Bucket.ApplyT(func(b string) string { return fmt.Sprintf("s3://%s/curated/loseit_parquet/", b) }).(pulumi.StringOutput)},
			},
			// Run every Sunday one hour before the weekly report (17:00 UTC / 6 pm London during DST).
			Schedule:    pulumi.String("cron(0 17 ? * SUN *)"),
			TablePrefix: pulumi.String("loseit_"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Transform Lambda: CSV -> Parquet (Snappy)
		transformRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-transform-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(lambdaAssumeRolePolicy.Json),
		}, awsOpts)
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-%s-transform-basic", project, stack), &iam.RolePolicyAttachmentArgs{
			Role:      transformRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Create S3 access policy for transform Lambda
		transformS3PolicyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
			Statements: []iam.GetPolicyDocumentStatement{
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:GetObject",
						"s3:PutObject",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName + "/*"},
				},
				{
					Effect: pulumi.StringRef("Allow"),
					Actions: []string{
						"s3:ListBucket",
					},
					Resources: []string{"arn:aws:s3:::" + dataBucketName},
				},
			},
		}, nil)
		if err != nil {
			return err
		}

		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-transform-s3", project, stack), &iam.RolePolicyArgs{
			Role:   transformRole.ID(),
			Policy: pulumi.String(transformS3PolicyDoc.Json),
		}, awsOpts)
		if err != nil {
			return err
		}

		transformZip := pulumi.NewFileArchive("../dist/loseit_transform.zip")
		transformFn, err := lambda.NewFunction(ctx, fmt.Sprintf("%s-%s-loseit-transform", project, stack), &lambda.FunctionArgs{
			Role:          transformRole.Arn,
			Runtime:       pulumi.String("provided.al2"),
			Handler:       pulumi.String("bootstrap"),
			Architectures: pulumi.ToStringArray([]string{"arm64"}),
			Code:          transformZip,
			Environment: &lambda.FunctionEnvironmentArgs{
				Variables: pulumi.StringMap{
					"DATA_BUCKET":  emailsBucket.Bucket,
					"RAW_CSV_BASE": pulumi.String("raw/loseit_csv/"),
					"CURATED_BASE": pulumi.String("curated/loseit_parquet/"),
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		ctx.Export("transformLambda", transformFn.Name)

		_, err = lambda.NewPermission(ctx, fmt.Sprintf("%s-%s-transform-perm", project, stack), &lambda.PermissionArgs{
			Action:    pulumi.String("lambda:InvokeFunction"),
			Function:  transformFn.Name,
			Principal: pulumi.String("s3.amazonaws.com"),
			SourceArn: emailsBucket.Arn,
		}, awsOpts)
		if err != nil {
			return err
		}

		// S3 BucketNotification with both email ingest and transform Lambda triggers
		_, err = s3.NewBucketNotification(ctx, fmt.Sprintf("%s-%s-data-notify", project, stack), &s3.BucketNotificationArgs{
			Bucket: emailsBucket.ID(),
			LambdaFunctions: s3.BucketNotificationLambdaFunctionArray{
				&s3.BucketNotificationLambdaFunctionArgs{
					LambdaFunctionArn: emailIngestFn.Arn,
					Events:            pulumi.ToStringArray([]string{"s3:ObjectCreated:*"}),
					FilterPrefix:      pulumi.String("raw/email/incoming/"),
				},
				&s3.BucketNotificationLambdaFunctionArgs{
					LambdaFunctionArn: transformFn.Arn,
					Events:            pulumi.ToStringArray([]string{"s3:ObjectCreated:*"}),
					FilterPrefix:      pulumi.String("raw/loseit_csv/"),
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		// Optional: set up SES receiving to S3 for a specific recipient address
		if recipient, ok := ctx.GetConfig("mailmunch:recipientAddress"); ok && recipient != "" {
			// Create (or ensure) a receipt rule set and rule to write to S3 prefix raw/email/incoming/
			ruleSetName := fmt.Sprintf("%s-%s-receipt-set", project, stack)
			ruleSet, err := ses.NewReceiptRuleSet(ctx, ruleSetName, &ses.ReceiptRuleSetArgs{
				RuleSetName: pulumi.String(ruleSetName),
			}, awsOpts)
			if err != nil {
				return err
			}

			// Activate the rule set
			_, err = ses.NewActiveReceiptRuleSet(ctx, fmt.Sprintf("%s-%s-receipt-active", project, stack), &ses.ActiveReceiptRuleSetArgs{
				RuleSetName: ruleSet.RuleSetName,
			}, awsOpts)
			if err != nil {
				return err
			}

			_, err = ses.NewReceiptRule(ctx, fmt.Sprintf("%s-%s-receipt-rule", project, stack), &ses.ReceiptRuleArgs{
				RuleSetName: ruleSet.RuleSetName,
				Recipients:  pulumi.ToStringArray([]string{recipient}),
				Enabled:     pulumi.Bool(true),
				ScanEnabled: pulumi.Bool(true),
				S3Actions: ses.ReceiptRuleS3ActionArray{
					&ses.ReceiptRuleS3ActionArgs{
						BucketName:      emailsBucket.Bucket,
						ObjectKeyPrefix: pulumi.String("raw/email/incoming/"),
						Position:        pulumi.Int(1),
					},
				},
				TlsPolicy: pulumi.String("Optional"),
			}, awsOpts)
			if err != nil {
				return err
			}

			ctx.Export("sesRecipient", pulumi.String(recipient))
			ctx.Export("sesRuleSet", ruleSet.RuleSetName)
		}
		return nil
	})
}
