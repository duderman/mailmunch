package main

import (
	"fmt"

	aws "github.com/pulumi/pulumi-aws/sdk/v6/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/appconfig"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ecr"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/secretsmanager"
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

		role, err := iam.NewRole(ctx, fmt.Sprintf("%s-%s-lambda-role", project, stack), &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
			  "Version": "2012-10-17",
			  "Statement": [
			    {
			      "Action": "sts:AssumeRole",
			      "Principal": {
			        "Service": "lambda.amazonaws.com"
			      },
			      "Effect": "Allow",
			      "Sid": ""
			    }
			  ]
			}`),
		}, awsOpts)
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-%s-lambda-basic", project, stack), &iam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
		}, awsOpts)
		if err != nil {
			return err
		}

		// Allow reading secrets and appconfig
		_, err = iam.NewRolePolicy(ctx, fmt.Sprintf("%s-%s-lambda-inline", project, stack), &iam.RolePolicyArgs{
			Role: role.ID(),
			Policy: secret.Arn.ApplyT(func(arn string) string {
				return fmt.Sprintf(`{
		  "Version": "2012-10-17",
		  "Statement": [
			{"Effect":"Allow","Action":["secretsmanager:GetSecretValue"],"Resource":"%s"},
			{"Effect":"Allow","Action":["appconfig:GetConfiguration*","appconfig:StartConfigurationSession"],"Resource":"*"}
		  ]
		}`, arn)
			}).(pulumi.StringOutput),
		}, awsOpts)
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

		// Package path for Lambda code
		lambdaZip := pulumi.NewFileArchive("../dist/hello.zip")

		fn, err := lambda.NewFunction(ctx, fmt.Sprintf("%s-%s-hello", project, stack), &lambda.FunctionArgs{
			Role:          role.Arn,
			Runtime:       pulumi.String("provided.al2"),
			Handler:       pulumi.String("bootstrap"),
			Architectures: pulumi.ToStringArray([]string{"arm64"}),
			Code:          lambdaZip,
			Environment: &lambda.FunctionEnvironmentArgs{
				Variables: pulumi.StringMap{
					"SECRET_ARN": secret.Arn,
					"BUCKET":     bucket.Bucket,
				},
			},
		}, awsOpts)
		if err != nil {
			return err
		}

		ctx.Export("bucketName", bucket.Bucket)
		ctx.Export("ecrRepositoryUrl", repo.RepositoryUrl)
		ctx.Export("secretArn", secret.Arn)
		ctx.Export("lambdaName", fn.Name)
		ctx.Export("region", aws.GetRegionOutput(ctx, aws.GetRegionOutputArgs{}).Name())
		if v, ok := ctx.GetConfig("mailmunch:sesEmailIdentity"); ok {
			ctx.Export("sesEmailIdentity", pulumi.String(v))
		}
		return nil
	})
}
