package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/appconfigdata"
	"github.com/aws/aws-sdk-go/service/athena"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/ses"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// WeeklyReportEvent represents the EventBridge event that triggers this Lambda
type WeeklyReportEvent struct {
	Source     string    `json:"source"`
	DetailType string    `json:"detail-type"`
	Detail     any       `json:"detail"`
	Time       time.Time `json:"time"`
}

// WeeklyData represents raw food data for a week period
type WeeklyData struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	RawData   string `json:"raw_data"` // Raw CSV-like data from Athena query
}

// Config holds environment variables and configuration
type Config struct {
	OpenAISecretArn        string
	ReportEmail            string
	SenderEmail            string
	Region                 string
	SystemPrompt           string
	BasePrompt             string
	AthenaDatabase         string
	AthenaTable            string
	AthenaWorkgroup        string
	AthenaResultsBucket    string
	AppConfigApplication   string
	AppConfigEnvironment   string
	AppConfigConfiguration string
}

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, event events.CloudWatchEvent) error {
	config := &Config{
		OpenAISecretArn:        getEnvOrDefault("OPENAI_SECRET_ARN", ""),
		ReportEmail:            getEnvOrDefault("REPORT_EMAIL", ""),
		SenderEmail:            getEnvOrDefault("SENDER_EMAIL", ""),
		Region:                 getEnvOrDefault("AWS_REGION", "eu-west-2"),
		AthenaDatabase:         getEnvOrDefault("ATHENA_DATABASE", "mailmunch_dev_db"),
		AthenaTable:            getEnvOrDefault("ATHENA_TABLE", "loseit_entries"),
		AthenaWorkgroup:        getEnvOrDefault("ATHENA_WORKGROUP", "primary"),
		AthenaResultsBucket:    getEnvOrDefault("ATHENA_RESULTS_BUCKET", ""),
		AppConfigApplication:   getEnvOrDefault("APPCONFIG_APPLICATION", ""),
		AppConfigEnvironment:   getEnvOrDefault("APPCONFIG_ENVIRONMENT", ""),
		AppConfigConfiguration: getEnvOrDefault("APPCONFIG_CONFIGURATION", ""),
	}

	if err := validateConfig(config); err != nil {
		log.Printf("Configuration error: %v", err)
		return err
	}

	log.Printf("Starting weekly report generation for email: %s", config.ReportEmail)

	// Calculate date ranges for current and previous weeks
	now := time.Now().In(londonTimeZone())
	currentWeekStart, currentWeekEnd := getWeekRange(now)
	previousWeekStart, previousWeekEnd := getWeekRange(currentWeekStart.AddDate(0, 0, -7))

	log.Printf("Current week: %s to %s", currentWeekStart.Format("2006-01-02"), currentWeekEnd.Format("2006-01-02"))
	log.Printf("Previous week: %s to %s", previousWeekStart.Format("2006-01-02"), previousWeekEnd.Format("2006-01-02"))

	// Initialize AWS session
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(config.Region),
	})
	if err != nil {
		log.Printf("Failed to create AWS session: %v", err)
		return err
	}

	sesClient := ses.New(sess)
	secretsClient := secretsmanager.New(sess)
	athenaClient := athena.New(sess)
	appConfigClient := appconfigdata.New(sess)

	// Get prompt configuration from AppConfig
	config.BasePrompt, config.SystemPrompt, err = getPromptsFromAppConfig(appConfigClient, config)
	if err != nil {
		log.Printf("Failed to retrieve prompt from AppConfig: %v", err)
		return err
	}

	// Retrieve OpenAI API key from Secrets Manager
	openaiAPIKey, err := getOpenAIAPIKey(secretsClient, config.OpenAISecretArn)
	if err != nil {
		log.Printf("Failed to retrieve OpenAI API key: %v", err)
		return err
	}

	// Query data for both weeks using Athena
	currentWeekData, err := queryWeeklyDataWithAthena(ctx, athenaClient, config, currentWeekStart, currentWeekEnd)
	if err != nil {
		log.Printf("Failed to query current week data: %v", err)
		return err
	}

	previousWeekData, err := queryWeeklyDataWithAthena(ctx, athenaClient, config, previousWeekStart, previousWeekEnd)
	if err != nil {
		log.Printf("Failed to query previous week data: %v", err)
		return err
	}

	// Generate OpenAI analysis
	report, err := generateAIReport(openaiAPIKey, config, currentWeekData, previousWeekData)
	if err != nil {
		log.Printf("Failed to generate AI report: %v", err)
		return err
	}

	// Send email report
	err = sendEmailReport(sesClient, config, report, currentWeekData, previousWeekData)
	if err != nil {
		log.Printf("Failed to send email report: %v", err)
		return err
	}

	log.Printf("Weekly report sent successfully to %s", config.ReportEmail)
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func validateConfig(config *Config) error {
	if config.OpenAISecretArn == "" {
		return fmt.Errorf("OPENAI_SECRET_ARN environment variable is required")
	}
	if config.ReportEmail == "" {
		return fmt.Errorf("REPORT_EMAIL environment variable is required")
	}
	if config.SenderEmail == "" {
		return fmt.Errorf("SENDER_EMAIL environment variable is required")
	}
	if config.AppConfigApplication == "" {
		return fmt.Errorf("APPCONFIG_APPLICATION environment variable is required")
	}
	if config.AppConfigEnvironment == "" {
		return fmt.Errorf("APPCONFIG_ENVIRONMENT environment variable is required")
	}
	if config.AppConfigConfiguration == "" {
		return fmt.Errorf("APPCONFIG_CONFIGURATION environment variable is required")
	}
	return nil
}

func getOpenAIAPIKey(secretsClient *secretsmanager.SecretsManager, secretArn string) (string, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretArn),
	}

	result, err := secretsClient.GetSecretValue(input)
	if err != nil {
		return "", fmt.Errorf("failed to get secret value: %w", err)
	}

	if result.SecretString == nil {
		return "", fmt.Errorf("secret value is empty")
	}

	// Parse JSON if the secret is stored as JSON
	var secretData map[string]string
	if err := json.Unmarshal([]byte(*result.SecretString), &secretData); err == nil {
		// If it's JSON, look for the "openai_api_key" field
		if apiKey, exists := secretData["openai_api_key"]; exists {
			return apiKey, nil
		}
		return "", fmt.Errorf("openai_api_key field not found in secret JSON")
	}

	// If it's not JSON, treat the entire secret as the API key
	return *result.SecretString, nil
}

func getPromptsFromAppConfig(appConfigClient *appconfigdata.AppConfigData, config *Config) (string, string, error) {
	// Start a configuration session
	sessionInput := &appconfigdata.StartConfigurationSessionInput{
		ApplicationIdentifier:          aws.String(config.AppConfigApplication),
		EnvironmentIdentifier:          aws.String(config.AppConfigEnvironment),
		ConfigurationProfileIdentifier: aws.String(config.AppConfigConfiguration),
	}

	sessionResult, err := appConfigClient.StartConfigurationSession(sessionInput)
	if err != nil {
		return "", "", fmt.Errorf("failed to start configuration session: %w", err)
	}

	// Get the latest configuration
	configInput := &appconfigdata.GetLatestConfigurationInput{
		ConfigurationToken: sessionResult.InitialConfigurationToken,
	}

	result, err := appConfigClient.GetLatestConfiguration(configInput)
	if err != nil {
		return "", "", fmt.Errorf("failed to get latest configuration from AppConfig: %w", err)
	}

	// Parse the JSON configuration
	var configData map[string]string
	if err := json.Unmarshal(result.Configuration, &configData); err != nil {
		return "", "", fmt.Errorf("failed to parse AppConfig content as JSON: %w", err)
	}

	// Look for the weekly_report_base_prompt field
	basePrompt, baseOk := configData["weekly_report_base_prompt"]
	systemPrompt, sysOk := configData["weekly_report_system_prompt"]

	if !baseOk {
		return "", "", fmt.Errorf("weekly_report_base_prompt field not found in AppConfig")
	}

	if !sysOk {
		return "", "", fmt.Errorf("weekly_report_system_prompt field not found in AppConfig")
	}

	return basePrompt, systemPrompt, nil
}

func londonTimeZone() *time.Location {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		log.Printf("Warning: Failed to load London timezone, using UTC: %v", err)
		return time.UTC
	}
	return loc
}

func getWeekRange(date time.Time) (start, end time.Time) {
	// Get Monday of the week (start of week)
	weekday := date.Weekday()
	daysFromMonday := int(weekday - time.Monday)
	if weekday == time.Sunday {
		daysFromMonday = 6 // Sunday is -1 day from Monday, so we go back 6 days
	}

	start = date.AddDate(0, 0, -daysFromMonday)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())

	end = start.AddDate(0, 0, 6)
	end = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 999999999, end.Location())

	return start, end
}

// queryWeeklyDataWithAthena executes an Athena query to get raw food data for the specified week
func queryWeeklyDataWithAthena(ctx context.Context, athenaClient *athena.Athena, config *Config, startDate, endDate time.Time) (*WeeklyData, error) {
	query := fmt.Sprintf(`
		SELECT
			"name=date" AS date,
			"name=name" AS food_name,
			"name=quantity" AS quantity,
			"name=units" AS unit,
			"name=calories" AS calories,
			"name=protein_g" AS protein,
			"name=carbs_g" AS carbs,
			"name=fat_g" AS fat,
			"name=fiber_g" AS fiber,
			"name=sugar_g" AS sugar,
			"name=sodium_mg" AS sodium
		FROM %s.%s
		WHERE "name=record_type" <> 'Exercise'
			AND date_parse("name=date", '%%m/%%d/%%Y') BETWEEN date '%s' AND date '%s'
		ORDER BY date, food_name
	`, config.AthenaDatabase, config.AthenaTable, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	queryExecutionID, err := executeAthenaQuery(ctx, athenaClient, config, query)
	if err != nil {
		return nil, err
	}

	if err := waitForAthenaQueryCompletion(ctx, athenaClient, queryExecutionID); err != nil {
		return nil, err
	}

	// Get query results
	results, err := athenaClient.GetQueryResultsWithContext(ctx, &athena.GetQueryResultsInput{
		QueryExecutionId: aws.String(queryExecutionID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get query results: %v", err)
	}

	// Convert results to CSV-like format for OpenAI
	var rawData strings.Builder

	// Add header row
	rawData.WriteString("date,food_name,quantity,unit,calories,protein,carbs,fat,fiber,sugar,sodium\n")

	// Skip header row in results and add data rows
	for i, row := range results.ResultSet.Rows {
		if i == 0 {
			continue // Skip header row
		}

		var values []string
		for _, col := range row.Data {
			if col.VarCharValue != nil {
				values = append(values, *col.VarCharValue)
			} else {
				values = append(values, "")
			}
		}
		rawData.WriteString(strings.Join(values, ",") + "\n")
	}

	return &WeeklyData{
		StartDate: startDate.Format("2006-01-02"),
		EndDate:   endDate.Format("2006-01-02"),
		RawData:   rawData.String(),
	}, nil
}

func executeAthenaQuery(ctx context.Context, athenaClient *athena.Athena, config *Config, query string) (string, error) {
	result, err := athenaClient.StartQueryExecutionWithContext(ctx, &athena.StartQueryExecutionInput{
		QueryString: aws.String(query),
		WorkGroup:   aws.String(config.AthenaWorkgroup),
		ResultConfiguration: &athena.ResultConfiguration{
			OutputLocation: aws.String(fmt.Sprintf("s3://%s/athena-results/", config.AthenaResultsBucket)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to start Athena query execution: %w", err)
	}
	if result.QueryExecutionId == nil {
		return "", fmt.Errorf("athena did not return a query execution ID")
	}
	return *result.QueryExecutionId, nil
}

func waitForAthenaQueryCompletion(ctx context.Context, athenaClient *athena.Athena, queryExecutionID string) error {
	for {
		result, err := athenaClient.GetQueryExecutionWithContext(ctx, &athena.GetQueryExecutionInput{
			QueryExecutionId: aws.String(queryExecutionID),
		})
		if err != nil {
			return fmt.Errorf("failed to get query execution status: %w", err)
		}

		statusInfo := result.QueryExecution.Status
		status := aws.StringValue(statusInfo.State)
		if status == athena.QueryExecutionStateSucceeded {
			return nil
		}
		if status == athena.QueryExecutionStateFailed || status == athena.QueryExecutionStateCancelled {
			reason := strings.TrimSpace(aws.StringValue(statusInfo.StateChangeReason))
			if statusInfo.AthenaError != nil {
				errTypeStr := ""
				if statusInfo.AthenaError.ErrorType != nil {
					errTypeStr = fmt.Sprintf("type=%d", aws.Int64Value(statusInfo.AthenaError.ErrorType))
				}
				errMsg := strings.TrimSpace(aws.StringValue(statusInfo.AthenaError.ErrorMessage))
				formatted := ""
				switch {
				case errTypeStr != "" && errMsg != "":
					formatted = fmt.Sprintf("%s: %s", errTypeStr, errMsg)
				case errTypeStr != "":
					formatted = errTypeStr
				case errMsg != "":
					formatted = errMsg
				}
				if formatted != "" {
					if reason != "" {
						reason = fmt.Sprintf("%s; %s", reason, formatted)
					} else {
						reason = formatted
					}
				}
			}
			if reason == "" {
				reason = "unknown"
			}
			log.Printf("Athena query failed (status=%s): %s", status, reason)
			return fmt.Errorf("query execution failed with status: %s, reason: %s", status, reason)
		}

		// Wait before checking again
		time.Sleep(1 * time.Second)
	}
}

const openAIChatModel = "gpt-5"

func generateAIReport(openaiAPIKey string, config *Config, currentWeek, previousWeek *WeeklyData) (string, error) {
	client := openai.NewClient(
		option.WithAPIKey(openaiAPIKey),
	)

	// Prepare data for OpenAI
	prompt := buildAnalysisPrompt(config.BasePrompt, currentWeek, previousWeek)

	log.Printf("Sending request to OpenAI with %d chars prompt", len(prompt))

	resp, err := client.Chat.Completions.New(
		context.Background(),
		openai.ChatCompletionNewParams{
			Model: shared.ChatModel(openAIChatModel),
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfSystem: &openai.ChatCompletionSystemMessageParam{
						Content: openai.ChatCompletionSystemMessageParamContentUnion{
							OfString: openai.String(config.SystemPrompt),
						},
					},
				},
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.ChatCompletionUserMessageParamContentUnion{
							OfString: openai.String(prompt),
						},
					},
				},
			},
			MaxCompletionTokens: openai.Int(20000),
		},
	)

	if err != nil {
		return "", fmt.Errorf("OpenAI API error: %w", err)
	}

	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	choice := resp.Choices[0]

	log.Printf(
		"OpenAI completion usage: prompt=%d completion=%d total=%d (finish_reason=%s)",
		resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens,
		resp.Usage.TotalTokens,
		choice.FinishReason,
	)

	if refusal := strings.TrimSpace(choice.Message.Refusal); refusal != "" {
		log.Printf("OpenAI refusal detected; first 160 chars: %s", truncateString(refusal, 160))
	}

	analysis := extractAssistantContent(choice.Message)
	if len(strings.TrimSpace(analysis)) == 0 {
		log.Printf("Warning: OpenAI returned empty assistant content for weekly report prompt")
	}
	log.Printf("Received %d chars analysis from OpenAI", len(analysis))

	return analysis, nil
}

// truncateString guards log messages from flooding CloudWatch when refusals are verbose.
func truncateString(input string, maxLen int) string {
	if len(input) <= maxLen {
		return input
	}
	if maxLen <= 3 {
		return input[:maxLen]
	}
	return input[:maxLen-3] + "..."
}

func extractAssistantContent(msg openai.ChatCompletionMessage) string {
	if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
		return msg.Content
	}
	if trimmed := strings.TrimSpace(msg.Refusal); trimmed != "" {
		return trimmed
	}
	return ""
}

func buildAnalysisPrompt(basePrompt string, currentWeek, previousWeek *WeeklyData) string {
	var builder strings.Builder

	builder.WriteString(basePrompt)
	builder.WriteString("\n\n")

	// Current week raw data
	builder.WriteString("## CURRENT WEEK RAW DATA (" + currentWeek.StartDate + " to " + currentWeek.EndDate + "):\n")
	builder.WriteString("```csv\n")
	builder.WriteString(currentWeek.RawData)
	builder.WriteString("```\n\n")

	// Previous week raw data for comparison
	builder.WriteString("## PREVIOUS WEEK RAW DATA (" + previousWeek.StartDate + " to " + previousWeek.EndDate + "):\n")
	builder.WriteString("```csv\n")
	builder.WriteString(previousWeek.RawData)
	builder.WriteString("```\n\n")

	return builder.String()
}

func sendEmailReport(sesClient *ses.SES, config *Config, analysis string, currentWeek, previousWeek *WeeklyData) error {
	subject := fmt.Sprintf("Weekly Nutrition Report - %s to %s", currentWeek.StartDate, currentWeek.EndDate)

	htmlBody, err := buildHTMLEmail(analysis, currentWeek, previousWeek)
	if err != nil {
		return fmt.Errorf("failed to build HTML email: %w", err)
	}
	textBody := buildTextEmail(analysis, currentWeek, previousWeek)

	input := &ses.SendEmailInput{
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(config.ReportEmail)},
		},
		Message: &ses.Message{
			Body: &ses.Body{
				Html: &ses.Content{
					Charset: aws.String("UTF-8"),
					Data:    aws.String(htmlBody),
				},
				Text: &ses.Content{
					Charset: aws.String("UTF-8"),
					Data:    aws.String(textBody),
				},
			},
			Subject: &ses.Content{
				Charset: aws.String("UTF-8"),
				Data:    aws.String(subject),
			},
		},
		Source: aws.String(config.SenderEmail),
	}

	result, err := sesClient.SendEmail(input)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Printf("Email sent successfully. MessageID: %s", *result.MessageId)
	return nil
}

type EmailData struct {
	CurrentWeek  *WeeklyData
	PreviousWeek *WeeklyData
	Analysis     string
}

func buildHTMLEmail(analysis string, currentWeek, previousWeek *WeeklyData) (string, error) {
	const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Weekly Nutrition Report</title>
    <style>
        body { font-family: Arial, sans-serif; line-height: 1.6; color: #333; }
        .container { max-width: 800px; margin: 0 auto; padding: 20px; }
        .header { background-color: #4CAF50; color: white; padding: 20px; text-align: center; }
        .summary { display: flex; justify-content: space-between; margin: 20px 0; }
        .week-card { background-color: #f9f9f9; padding: 15px; border-radius: 8px; flex: 1; margin: 0 10px; }
        .metrics { margin: 10px 0; }
        .metric { margin: 5px 0; }
        .analysis { background-color: #e8f5e8; padding: 20px; border-radius: 8px; margin: 20px 0; }
        .footer { text-align: center; margin-top: 30px; font-size: 12px; color: #666; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>Weekly Nutrition Report</h1>
            <p>{{.CurrentWeek.StartDate}} to {{.CurrentWeek.EndDate}}</p>
        </div>

        <div class="summary">
            <div class="week-card">
                <h3>Current Week ({{.CurrentWeek.StartDate}} to {{.CurrentWeek.EndDate}})</h3>
                <div class="metrics">
                    <div class="metric">Raw food data has been analyzed by AI below</div>
                </div>
            </div>

            <div class="week-card">
                <h3>Previous Week ({{.PreviousWeek.StartDate}} to {{.PreviousWeek.EndDate}})</h3>
                <div class="metrics">
                    <div class="metric">Used for comparison in AI analysis</div>
                </div>
            </div>
        </div>

        <div class="analysis">
            <h3>AI Analysis & Recommendations</h3>
            <div style="white-space: pre-wrap;">{{.Analysis}}</div>
        </div>

        <div class="footer">
            <p>Generated by MailMunch Weekly Report System</p>
        </div>
    </div>
</body>
</html>`

	tmpl, err := template.New("email").Parse(htmlTemplate)
	if err != nil {
		log.Printf("Error parsing email template: %v", err)
		return "", fmt.Errorf("failed to parse email template: %w", err)
	}

	data := EmailData{
		CurrentWeek:  currentWeek,
		PreviousWeek: previousWeek,
		Analysis:     analysis,
	}

	var buffer strings.Builder
	if err := tmpl.Execute(&buffer, data); err != nil {
		log.Printf("Error executing email template: %v", err)
		return "", fmt.Errorf("failed to execute email template: %w", err)
	}

	return buffer.String(), nil
}

func buildTextEmail(analysis string, currentWeek, previousWeek *WeeklyData) string {
	var builder strings.Builder

	builder.WriteString("WEEKLY NUTRITION REPORT\n")
	builder.WriteString("=" + strings.Repeat("=", 50) + "\n\n")

	builder.WriteString("Report Period: " + currentWeek.StartDate + " to " + currentWeek.EndDate + "\n\n")

	builder.WriteString("AI ANALYSIS & RECOMMENDATIONS:\n")
	builder.WriteString("-" + strings.Repeat("-", 40) + "\n")
	builder.WriteString(analysis)
	builder.WriteString("\n\n")

	builder.WriteString("Generated by MailMunch Weekly Report System\n")

	return builder.String()
}
