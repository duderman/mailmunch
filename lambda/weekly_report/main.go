package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/sashabaranov/go-openai"
)

// WeeklyReportEvent represents the EventBridge event that triggers this Lambda
type WeeklyReportEvent struct {
	Source      string    `json:"source"`
	DetailType  string    `json:"detail-type"`
	Detail      any       `json:"detail"`
	Time        time.Time `json:"time"`
}

// FoodEntry represents a single food log entry
type FoodEntry struct {
	Date        string  `json:"date"`
	FoodName    string  `json:"food_name"`
	Quantity    float64 `json:"quantity"`
	Unit        string  `json:"unit"`
	Calories    float64 `json:"calories"`
	Protein     float64 `json:"protein"`
	Carbs       float64 `json:"carbs"`
	Fat         float64 `json:"fat"`
	Fiber       float64 `json:"fiber"`
	Sugar       float64 `json:"sugar"`
	Sodium      float64 `json:"sodium"`
}

// WeeklyData represents aggregated data for a week
type WeeklyData struct {
	StartDate     string      `json:"start_date"`
	EndDate       string      `json:"end_date"`
	TotalCalories float64     `json:"total_calories"`
	TotalProtein  float64     `json:"total_protein"`
	TotalCarbs    float64     `json:"total_carbs"`
	TotalFat      float64     `json:"total_fat"`
	TotalFiber    float64     `json:"total_fiber"`
	TotalSugar    float64     `json:"total_sugar"`
	TotalSodium   float64     `json:"total_sodium"`
	DailyEntries  []FoodEntry `json:"daily_entries"`
}

// Config holds environment variables and configuration
type Config struct {
	DataBucket      string
	OpenAISecretArn string
	ReportEmail     string
	SenderEmail     string
	Region          string
	BasePrompt      string
}

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, event events.CloudWatchEvent) error {
	config := &Config{
		DataBucket:      getEnvOrDefault("DATA_BUCKET", ""),
		OpenAISecretArn: getEnvOrDefault("OPENAI_SECRET_ARN", ""),
		ReportEmail:     getEnvOrDefault("REPORT_EMAIL", ""),
		SenderEmail:     getEnvOrDefault("SENDER_EMAIL", ""),
		Region:          getEnvOrDefault("AWS_REGION", "eu-west-2"),
		BasePrompt:      getEnvOrDefault("BASE_PROMPT", getDefaultPrompt()),
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

	s3Client := s3.New(sess)
	sesClient := ses.New(sess)
	secretsClient := secretsmanager.New(sess)

	// Retrieve OpenAI API key from Secrets Manager
	openaiAPIKey, err := getOpenAIAPIKey(secretsClient, config.OpenAISecretArn)
	if err != nil {
		log.Printf("Failed to retrieve OpenAI API key: %v", err)
		return err
	}

	// Query data for both weeks
	currentWeekData, err := queryWeeklyData(s3Client, config.DataBucket, currentWeekStart, currentWeekEnd)
	if err != nil {
		log.Printf("Failed to query current week data: %v", err)
		return err
	}

	previousWeekData, err := queryWeeklyData(s3Client, config.DataBucket, previousWeekStart, previousWeekEnd)
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
	if config.DataBucket == "" {
		return fmt.Errorf("DATA_BUCKET environment variable is required")
	}
	if config.OpenAISecretArn == "" {
		return fmt.Errorf("OPENAI_SECRET_ARN environment variable is required")
	}
	if config.ReportEmail == "" {
		return fmt.Errorf("REPORT_EMAIL environment variable is required")
	}
	if config.SenderEmail == "" {
		return fmt.Errorf("SENDER_EMAIL environment variable is required")
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

func queryWeeklyData(s3Client *s3.S3, bucket string, startDate, endDate time.Time) (*WeeklyData, error) {
	log.Printf("Querying data from %s to %s", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	
	weeklyData := &WeeklyData{
		StartDate:    startDate.Format("2006-01-02"),
		EndDate:      endDate.Format("2006-01-02"),
		DailyEntries: []FoodEntry{},
	}

	// Iterate through each day in the week
	currentDate := startDate
	for currentDate.Before(endDate) || currentDate.Equal(endDate) {
		dateStr := currentDate.Format("2006-01-02")
		
		// Query CSV files for this date from the curated data
		prefix := fmt.Sprintf("curated/loseit_csv/year=%d/month=%02d/day=%02d/",
			currentDate.Year(), currentDate.Month(), currentDate.Day())
		
		log.Printf("Searching for data with prefix: %s", prefix)
		
		input := &s3.ListObjectsV2Input{
			Bucket: aws.String(bucket),
			Prefix: aws.String(prefix),
		}

		result, err := s3Client.ListObjectsV2(input)
		if err != nil {
			log.Printf("Warning: Failed to list objects for %s: %v", dateStr, err)
			currentDate = currentDate.AddDate(0, 0, 1)
			continue
		}

		// Process each CSV file found for this date
		for _, obj := range result.Contents {
			entries, err := readCSVFromS3(s3Client, bucket, *obj.Key)
			if err != nil {
				log.Printf("Warning: Failed to read CSV %s: %v", *obj.Key, err)
				continue
			}

			for _, entry := range entries {
				entry.Date = dateStr // Ensure consistent date format
				weeklyData.DailyEntries = append(weeklyData.DailyEntries, entry)
				
				// Aggregate totals
				weeklyData.TotalCalories += entry.Calories
				weeklyData.TotalProtein += entry.Protein
				weeklyData.TotalCarbs += entry.Carbs
				weeklyData.TotalFat += entry.Fat
				weeklyData.TotalFiber += entry.Fiber
				weeklyData.TotalSugar += entry.Sugar
				weeklyData.TotalSodium += entry.Sodium
			}
		}

		currentDate = currentDate.AddDate(0, 0, 1)
	}

	log.Printf("Found %d food entries for week %s to %s", 
		len(weeklyData.DailyEntries), weeklyData.StartDate, weeklyData.EndDate)
	
	return weeklyData, nil
}

func readCSVFromS3(s3Client *s3.S3, bucket, key string) ([]FoodEntry, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	result, err := s3Client.GetObject(input)
	if err != nil {
		return nil, fmt.Errorf("failed to get object %s: %w", key, err)
	}
	defer result.Body.Close()

	reader := csv.NewReader(result.Body)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV: %w", err)
	}

	if len(records) == 0 {
		return []FoodEntry{}, nil
	}

	// Skip header row
	entries := make([]FoodEntry, 0, len(records)-1)
	for i, record := range records {
		if i == 0 {
			continue // Skip header
		}

		if len(record) < 10 {
			log.Printf("Warning: Skipping malformed CSV record with %d fields", len(record))
			continue
		}

		entry := FoodEntry{
			FoodName: record[1],
			Unit:     record[3],
		}

		// Parse numeric fields with error handling
		if val, err := parseFloat(record[2]); err == nil {
			entry.Quantity = val
		}
		if val, err := parseFloat(record[4]); err == nil {
			entry.Calories = val
		}
		if val, err := parseFloat(record[5]); err == nil {
			entry.Protein = val
		}
		if val, err := parseFloat(record[6]); err == nil {
			entry.Carbs = val
		}
		if val, err := parseFloat(record[7]); err == nil {
			entry.Fat = val
		}
		if val, err := parseFloat(record[8]); err == nil {
			entry.Fiber = val
		}
		if val, err := parseFloat(record[9]); err == nil {
			entry.Sugar = val
		}
		if len(record) > 10 {
			if val, err := parseFloat(record[10]); err == nil {
				entry.Sodium = val
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func parseFloat(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func generateAIReport(openaiAPIKey string, config *Config, currentWeek, previousWeek *WeeklyData) (string, error) {
	client := openai.NewClient(openaiAPIKey)

	// Prepare data for OpenAI
	prompt := buildAnalysisPrompt(config.BasePrompt, currentWeek, previousWeek)

	log.Printf("Sending request to OpenAI with %d chars prompt", len(prompt))

	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: openai.GPT4o,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: "You are a helpful nutritionist and fitness coach who provides detailed, actionable advice based on food diary data.",
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
			MaxTokens:   2000,
			Temperature: 0.7,
		},
	)

	if err != nil {
		return "", fmt.Errorf("OpenAI API error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	analysis := resp.Choices[0].Message.Content
	log.Printf("Received %d chars analysis from OpenAI", len(analysis))
	
	return analysis, nil
}

func buildAnalysisPrompt(basePrompt string, currentWeek, previousWeek *WeeklyData) string {
	var builder strings.Builder

	builder.WriteString(basePrompt)
	builder.WriteString("\n\n")

	// Current week summary
	builder.WriteString("## CURRENT WEEK DATA (" + currentWeek.StartDate + " to " + currentWeek.EndDate + "):\n")
	builder.WriteString(fmt.Sprintf("Total Calories: %.1f\n", currentWeek.TotalCalories))
	builder.WriteString(fmt.Sprintf("Total Protein: %.1fg\n", currentWeek.TotalProtein))
	builder.WriteString(fmt.Sprintf("Total Carbs: %.1fg\n", currentWeek.TotalCarbs))
	builder.WriteString(fmt.Sprintf("Total Fat: %.1fg\n", currentWeek.TotalFat))
	builder.WriteString(fmt.Sprintf("Total Fiber: %.1fg\n", currentWeek.TotalFiber))
	builder.WriteString(fmt.Sprintf("Average daily calories: %.1f\n", currentWeek.TotalCalories/7))
	builder.WriteString(fmt.Sprintf("Number of food entries: %d\n\n", len(currentWeek.DailyEntries)))

	// Previous week comparison
	builder.WriteString("## PREVIOUS WEEK DATA (" + previousWeek.StartDate + " to " + previousWeek.EndDate + "):\n")
	builder.WriteString(fmt.Sprintf("Total Calories: %.1f\n", previousWeek.TotalCalories))
	builder.WriteString(fmt.Sprintf("Total Protein: %.1fg\n", previousWeek.TotalProtein))
	builder.WriteString(fmt.Sprintf("Total Carbs: %.1fg\n", previousWeek.TotalCarbs))
	builder.WriteString(fmt.Sprintf("Total Fat: %.1fg\n", previousWeek.TotalFat))
	builder.WriteString(fmt.Sprintf("Total Fiber: %.1fg\n", previousWeek.TotalFiber))
	builder.WriteString(fmt.Sprintf("Average daily calories: %.1f\n", previousWeek.TotalCalories/7))
	builder.WriteString(fmt.Sprintf("Number of food entries: %d\n\n", len(previousWeek.DailyEntries)))

	// Daily breakdown for current week (top foods)
	builder.WriteString("## CURRENT WEEK FOOD DETAILS:\n")
	dailyBreakdown := make(map[string][]FoodEntry)
	for _, entry := range currentWeek.DailyEntries {
		dailyBreakdown[entry.Date] = append(dailyBreakdown[entry.Date], entry)
	}

	// Sort dates
	dates := make([]string, 0, len(dailyBreakdown))
	for date := range dailyBreakdown {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	for _, date := range dates {
		entries := dailyBreakdown[date]
		dailyCalories := 0.0
		for _, entry := range entries {
			dailyCalories += entry.Calories
		}
		
		builder.WriteString(fmt.Sprintf("%s (%.0f calories):\n", date, dailyCalories))
		
		// Sort entries by calories to show most significant foods
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Calories > entries[j].Calories
		})
		
		// Show top 5 foods for the day
		maxEntries := 5
		if len(entries) < maxEntries {
			maxEntries = len(entries)
		}
		
		for i := 0; i < maxEntries; i++ {
			entry := entries[i]
			builder.WriteString(fmt.Sprintf("  - %s: %.1f cal, %.1fg protein, %.1fg carbs, %.1fg fat\n",
				entry.FoodName, entry.Calories, entry.Protein, entry.Carbs, entry.Fat))
		}
		
		if len(entries) > maxEntries {
			builder.WriteString(fmt.Sprintf("  ... and %d more items\n", len(entries)-maxEntries))
		}
		builder.WriteString("\n")
	}

	builder.WriteString("\nPlease analyze this data and provide detailed recommendations for weight loss and muscle growth.")
	
	return builder.String()
}

func sendEmailReport(sesClient *ses.SES, config *Config, analysis string, currentWeek, previousWeek *WeeklyData) error {
	subject := fmt.Sprintf("Weekly Nutrition Report - %s to %s", currentWeek.StartDate, currentWeek.EndDate)
	
	htmlBody := buildHTMLEmail(analysis, currentWeek, previousWeek)
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

	_, err := sesClient.SendEmail(input)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

func buildHTMLEmail(analysis string, currentWeek, previousWeek *WeeklyData) string {
	var builder strings.Builder

	builder.WriteString(`<!DOCTYPE html>
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
            <p>` + currentWeek.StartDate + ` to ` + currentWeek.EndDate + `</p>
        </div>
        
        <div class="summary">
            <div class="week-card">
                <h3>Current Week</h3>
                <div class="metrics">
                    <div class="metric"><strong>Total Calories:</strong> ` + fmt.Sprintf("%.0f", currentWeek.TotalCalories) + `</div>
                    <div class="metric"><strong>Avg Daily:</strong> ` + fmt.Sprintf("%.0f", currentWeek.TotalCalories/7) + ` cal</div>
                    <div class="metric"><strong>Protein:</strong> ` + fmt.Sprintf("%.1f", currentWeek.TotalProtein) + `g</div>
                    <div class="metric"><strong>Carbs:</strong> ` + fmt.Sprintf("%.1f", currentWeek.TotalCarbs) + `g</div>
                    <div class="metric"><strong>Fat:</strong> ` + fmt.Sprintf("%.1f", currentWeek.TotalFat) + `g</div>
                    <div class="metric"><strong>Fiber:</strong> ` + fmt.Sprintf("%.1f", currentWeek.TotalFiber) + `g</div>
                </div>
            </div>
            
            <div class="week-card">
                <h3>Previous Week</h3>
                <div class="metrics">
                    <div class="metric"><strong>Total Calories:</strong> ` + fmt.Sprintf("%.0f", previousWeek.TotalCalories) + `</div>
                    <div class="metric"><strong>Avg Daily:</strong> ` + fmt.Sprintf("%.0f", previousWeek.TotalCalories/7) + ` cal</div>
                    <div class="metric"><strong>Protein:</strong> ` + fmt.Sprintf("%.1f", previousWeek.TotalProtein) + `g</div>
                    <div class="metric"><strong>Carbs:</strong> ` + fmt.Sprintf("%.1f", previousWeek.TotalCarbs) + `g</div>
                    <div class="metric"><strong>Fat:</strong> ` + fmt.Sprintf("%.1f", previousWeek.TotalFat) + `g</div>
                    <div class="metric"><strong>Fiber:</strong> ` + fmt.Sprintf("%.1f", previousWeek.TotalFiber) + `g</div>
                </div>
            </div>
        </div>
        
        <div class="analysis">
            <h3>AI Analysis & Recommendations</h3>
            <div style="white-space: pre-wrap;">` + analysis + `</div>
        </div>
        
        <div class="footer">
            <p>Generated by MailMunch Weekly Report System</p>
        </div>
    </div>
</body>
</html>`)

	return builder.String()
}

func buildTextEmail(analysis string, currentWeek, previousWeek *WeeklyData) string {
	var builder strings.Builder

	builder.WriteString("WEEKLY NUTRITION REPORT\n")
	builder.WriteString("=" + strings.Repeat("=", 50) + "\n\n")
	
	builder.WriteString("Report Period: " + currentWeek.StartDate + " to " + currentWeek.EndDate + "\n\n")
	
	builder.WriteString("CURRENT WEEK SUMMARY:\n")
	builder.WriteString("-" + strings.Repeat("-", 30) + "\n")
	builder.WriteString(fmt.Sprintf("Total Calories: %.0f\n", currentWeek.TotalCalories))
	builder.WriteString(fmt.Sprintf("Average Daily: %.0f calories\n", currentWeek.TotalCalories/7))
	builder.WriteString(fmt.Sprintf("Protein: %.1fg\n", currentWeek.TotalProtein))
	builder.WriteString(fmt.Sprintf("Carbs: %.1fg\n", currentWeek.TotalCarbs))
	builder.WriteString(fmt.Sprintf("Fat: %.1fg\n", currentWeek.TotalFat))
	builder.WriteString(fmt.Sprintf("Fiber: %.1fg\n\n", currentWeek.TotalFiber))
	
	builder.WriteString("PREVIOUS WEEK COMPARISON:\n")
	builder.WriteString("-" + strings.Repeat("-", 30) + "\n")
	builder.WriteString(fmt.Sprintf("Total Calories: %.0f\n", previousWeek.TotalCalories))
	builder.WriteString(fmt.Sprintf("Average Daily: %.0f calories\n", previousWeek.TotalCalories/7))
	builder.WriteString(fmt.Sprintf("Protein: %.1fg\n", previousWeek.TotalProtein))
	builder.WriteString(fmt.Sprintf("Carbs: %.1fg\n", previousWeek.TotalCarbs))
	builder.WriteString(fmt.Sprintf("Fat: %.1fg\n", previousWeek.TotalFat))
	builder.WriteString(fmt.Sprintf("Fiber: %.1fg\n\n", previousWeek.TotalFiber))
	
	builder.WriteString("AI ANALYSIS & RECOMMENDATIONS:\n")
	builder.WriteString("-" + strings.Repeat("-", 40) + "\n")
	builder.WriteString(analysis)
	builder.WriteString("\n\n")
	
	builder.WriteString("Generated by MailMunch Weekly Report System\n")
	
	return builder.String()
}

func getDefaultPrompt() string {
	return `Please analyze my weekly food data and provide a comprehensive report with the following:

1. WEEKLY SUMMARY: Compare this week's nutrition to the previous week, highlighting key changes in calories, macronutrients, and overall diet quality.

2. WEIGHT LOSS RECOMMENDATIONS: Based on my food intake, suggest specific changes to support healthy weight loss including:
   - Calorie adjustments if needed
   - Food swaps for lower-calorie alternatives
   - Meal timing recommendations
   - Portion control suggestions

3. MUSCLE GROWTH RECOMMENDATIONS: Analyze my protein intake and suggest improvements for muscle building:
   - Protein targets and timing
   - Post-workout nutrition suggestions
   - Amino acid profile recommendations
   - Supplement considerations if applicable

4. FOOD QUALITY ANALYSIS: Evaluate the nutritional quality of my food choices:
   - Whole food vs processed food ratio
   - Micronutrient diversity
   - Fiber intake adequacy
   - Sugar and sodium levels

5. ACTIONABLE NEXT WEEK PLAN: Provide 3-5 specific, actionable steps I can take next week to improve my nutrition for both weight loss and muscle growth goals.

Please be specific, evidence-based, and practical in your recommendations. Consider that I'm looking to optimize my nutrition for both fat loss and muscle gain simultaneously.`
}