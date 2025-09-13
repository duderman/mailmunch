package main

import (
	"fmt"

	aws "github.com/pulumi/pulumi-aws/sdk/v6/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/appconfig"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ecr"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/glue"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/s3"
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
		_, err = appconfig.NewHostedConfigurationVersion(ctx, fmt.Sprintf("%s-%s-configv1", project, stack), &appconfig.HostedConfigurationVersionArgs{
			ApplicationId:          app.ID(),
			ConfigurationProfileId: profile.ConfigurationProfileId,
			Content:                pulumi.String("{\"greeting\":\"Hello from AppConfig\"}"),
			ContentType:            pulumi.String("application/json"),
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
			_, err = sesv2.NewEmailIdentity(ctx, fmt.Sprintf("%s-%s-ses-identity", project, stack), &sesv2.EmailIdentityArgs{
				EmailIdentity: pulumi.String(email),
			}, awsOpts)
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
		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-email-ingest-s3", project, stack), &iam.RolePolicyArgs{
			Role: ingestRole.ID(),
			Policy: emailsBucket.Arn.ApplyT(func(arn string) string {
				policyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
					Statements: []iam.GetPolicyDocumentStatement{
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:GetObject",
								"s3:PutObject",
							},
							Resources: []string{arn + "/*"},
						},
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:ListBucket",
							},
							Resources: []string{arn},
						},
					},
				}, nil)
				if err != nil {
					panic(err)
				}
				return policyDoc.Json
			}).(pulumi.StringOutput),
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
				policyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
					Version: pulumi.StringRef("2008-10-17"), // SES bucket policies use this older version
					Statements: []iam.GetPolicyDocumentStatement{
						{
							Sid:    pulumi.StringRef("AllowSESPuts"),
							Effect: pulumi.StringRef("Allow"),
							Principals: []iam.GetPolicyDocumentStatementPrincipal{
								{
									Type: "Service",
									Identifiers: []string{
										"ses.amazonaws.com",
									},
								},
							},
							Actions: []string{
								"s3:PutObject",
							},
							Resources: []string{arn + "/*"},
							Conditions: []iam.GetPolicyDocumentStatementCondition{
								{
									Test:     "StringEquals",
									Variable: "aws:Referer",
									Values:   []string{acct},
								},
							},
						},
					},
				}, nil)
				if err != nil {
					panic(err)
				}
				return policyDoc.Json
			}).(pulumi.StringOutput),
		}, awsOpts)
		if err != nil {
			return err
		}

		ctx.Export("bucketName", bucket.Bucket)
		ctx.Export("dataBucket", emailsBucket.Bucket)
		ctx.Export("ecrRepositoryUrl", repo.RepositoryUrl)
		ctx.Export("secretArn", secret.Arn)
		ctx.Export("emailIngestLambda", emailIngestFn.Name)
		ctx.Export("region", aws.GetRegionOutput(ctx, aws.GetRegionOutputArgs{}).Name())
		ctx.Export("allowedSenderDomain", pulumi.String(allowedSenderDomain))

		if v, ok := ctx.GetConfig("mailmunch:sesEmailIdentity"); ok {
			ctx.Export("sesEmailIdentity", pulumi.String(v))
		}

		// Glue database and crawler for curated Parquet
		glueDb, err := glue.NewCatalogDatabase(ctx, fmt.Sprintf("%s_%s_db", project, stack), &glue.CatalogDatabaseArgs{
			Name: pulumi.String(fmt.Sprintf("%s_%s", project, stack)),
		}, awsOpts)
		if err != nil {
			return err
		}

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
		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-glue-s3", project, stack), &iam.RolePolicyArgs{
			Role: glueRole.ID(),
			Policy: emailsBucket.Arn.ApplyT(func(arn string) string {
				policyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
					Statements: []iam.GetPolicyDocumentStatement{
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:GetObject",
							},
							Resources: []string{arn + "/curated/*"},
						},
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:ListBucket",
							},
							Resources: []string{arn},
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
					panic(err)
				}
				return policyDoc.Json
			}).(pulumi.StringOutput),
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
		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-transform-s3", project, stack), &iam.RolePolicyArgs{
			Role: transformRole.ID(),
			Policy: emailsBucket.Arn.ApplyT(func(arn string) string {
				policyDoc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
					Statements: []iam.GetPolicyDocumentStatement{
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:GetObject",
								"s3:PutObject",
							},
							Resources: []string{arn + "/*"},
						},
						{
							Effect: pulumi.StringRef("Allow"),
							Actions: []string{
								"s3:ListBucket",
							},
							Resources: []string{arn},
						},
					},
				}, nil)
				if err != nil {
					panic(err)
				}
				return policyDoc.Json
			}).(pulumi.StringOutput),
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
			ruleSet, err := ses.NewReceiptRuleSet(ctx, fmt.Sprintf("%s-%s-receipt-set", project, stack), &ses.ReceiptRuleSetArgs{}, awsOpts)
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
