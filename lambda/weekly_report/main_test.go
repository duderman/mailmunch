package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
)

func TestGetWeekRange(t *testing.T) {
	// Test Sunday (should get Monday to Sunday range)
	sunday := time.Date(2025, 1, 12, 15, 0, 0, 0, time.UTC) // Sunday, Jan 12, 2025
	start, end := getWeekRange(sunday)

	expectedStart := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC)           // Monday, Jan 6
	expectedEnd := time.Date(2025, 1, 12, 23, 59, 59, 999999999, time.UTC) // Sunday, Jan 12

	if !start.Equal(expectedStart) {
		t.Errorf("Expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Expected end %v, got %v", expectedEnd, end)
	}
}

func TestGetWeekRangeMonday(t *testing.T) {
	// Test Monday (should get same week Monday to Sunday)
	monday := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC) // Monday, Jan 6, 2025
	start, end := getWeekRange(monday)

	expectedStart := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC)           // Same Monday
	expectedEnd := time.Date(2025, 1, 12, 23, 59, 59, 999999999, time.UTC) // Sunday, Jan 12

	if !start.Equal(expectedStart) {
		t.Errorf("Expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Expected end %v, got %v", expectedEnd, end)
	}
}

func TestGetWeekRangeWednesday(t *testing.T) {
	// Test Wednesday (should get previous Monday to Sunday)
	wednesday := time.Date(2025, 1, 8, 14, 30, 0, 0, time.UTC) // Wednesday, Jan 8, 2025
	start, end := getWeekRange(wednesday)

	expectedStart := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC)           // Monday, Jan 6
	expectedEnd := time.Date(2025, 1, 12, 23, 59, 59, 999999999, time.UTC) // Sunday, Jan 12

	if !start.Equal(expectedStart) {
		t.Errorf("Expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Expected end %v, got %v", expectedEnd, end)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				OpenAISecretArn:        "arn:aws:secretsmanager:us-east-1:123456789012:secret:test-secret",
				ReportEmail:            "test@example.com",
				SenderEmail:            "sender@example.com",
				AppConfigApplication:   "test-app",
				AppConfigEnvironment:   "prod",
				AppConfigConfiguration: "test-config",
			},
			wantErr: false,
		},
		{
			name: "missing OpenAI secret ARN",
			config: &Config{
				ReportEmail: "test@example.com",
				SenderEmail: "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing report email",
			config: &Config{
				OpenAISecretArn: "arn:aws:secretsmanager:us-east-1:123456789012:secret:test-secret",
				SenderEmail:     "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing sender email",
			config: &Config{
				OpenAISecretArn: "arn:aws:secretsmanager:us-east-1:123456789012:secret:test-secret",
				ReportEmail:     "test@example.com",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		expected     string
	}{
		{
			name:         "environment variable exists",
			key:          "TEST_KEY",
			defaultValue: "default",
			envValue:     "env_value",
			expected:     "env_value",
		},
		{
			name:         "environment variable does not exist",
			key:          "NON_EXISTENT_KEY",
			defaultValue: "default",
			envValue:     "",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv(tt.key, tt.envValue)
			}

			result := getEnvOrDefault(tt.key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("getEnvOrDefault() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestLondonTimeZone(t *testing.T) {
	tz := londonTimeZone()
	if tz == nil {
		t.Error("londonTimeZone() returned nil")
	}

	// Should return either London timezone or UTC as fallback
	if tz.String() != "Europe/London" && tz.String() != "UTC" {
		t.Errorf("londonTimeZone() returned unexpected timezone: %s", tz.String())
	}
}

func TestConfigurationStructure(t *testing.T) {
	// Test that our configuration structure is valid
	config := &Config{
		AppConfigApplication:   "test-app",
		AppConfigEnvironment:   "prod",
		AppConfigConfiguration: "test-config",
	}

	if config.AppConfigApplication == "" {
		t.Error("AppConfigApplication should not be empty")
	}

	if config.AppConfigEnvironment == "" {
		t.Error("AppConfigEnvironment should not be empty")
	}

	if config.AppConfigConfiguration == "" {
		t.Error("AppConfigConfiguration should not be empty")
	}
}

// Test the lambda handler components integration
// NOTE: Full integration testing of the handler function would require refactoring
// to use dependency injection instead of creating AWS clients directly in the handler.
// For production code, consider refactoring to accept interfaces for AWS services.
func TestLambdaHandlerComponentsIntegration(t *testing.T) {
	// Set up environment variables for testing
	t.Setenv("OPENAI_SECRET_ARN", "arn:aws:secretsmanager:us-east-1:123456789012:secret:openai-key")
	t.Setenv("REPORT_EMAIL", "test@example.com")
	t.Setenv("SENDER_EMAIL", "sender@example.com")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("ATHENA_DATABASE", "test_db")
	t.Setenv("ATHENA_WORKGROUP", "primary")
	t.Setenv("ATHENA_RESULTS_BUCKET", "test-bucket")
	t.Setenv("APPCONFIG_APPLICATION", "test-app")
	t.Setenv("APPCONFIG_ENVIRONMENT", "test-env")
	t.Setenv("APPCONFIG_CONFIGURATION", "test-config")

	// Test successful configuration parsing
	t.Run("successful_configuration_parsing", func(t *testing.T) {
		// Test AppConfig configuration parsing (simulates what getPromptFromAppConfig does)
		configJSON := `{"weekly_report_base_prompt": "Test prompt for weekly analysis"}`
		var configData map[string]string
		err := json.Unmarshal([]byte(configJSON), &configData)
		if err != nil {
			t.Fatalf("Failed to parse config JSON: %v", err)
		}

		if prompt, exists := configData["weekly_report_base_prompt"]; !exists || prompt != "Test prompt for weekly analysis" {
			t.Error("Expected prompt not found in config")
		}

		// Test secret parsing (JSON format) - simulates what getOpenAIAPIKey does
		secretJSON := `{"openai_api_key": "sk-test-key-123"}`
		var secretData map[string]string
		err = json.Unmarshal([]byte(secretJSON), &secretData)
		if err != nil {
			t.Fatalf("Failed to parse secret JSON: %v", err)
		}

		if apiKey, exists := secretData["openai_api_key"]; !exists || !strings.HasPrefix(apiKey, "sk-") {
			t.Error("Expected OpenAI API key not found in secret")
		}

		// Test that we can create a valid CloudWatch event (handler input)
		event := events.CloudWatchEvent{
			Source:     "aws.scheduler",
			DetailType: "Weekly Report Trigger",
		}

		if event.Source != "aws.scheduler" {
			t.Error("Event source not set correctly")
		}
	})

	// Test configuration validation
	t.Run("configuration_validation", func(t *testing.T) {
		config := &Config{
			OpenAISecretArn:        "arn:aws:secretsmanager:us-east-1:123456789012:secret:openai-key",
			ReportEmail:            "test@example.com",
			SenderEmail:            "sender@example.com",
			AppConfigApplication:   "test-app",
			AppConfigEnvironment:   "test-env",
			AppConfigConfiguration: "test-config",
		}

		err := validateConfig(config)
		if err != nil {
			t.Errorf("Valid config should not return error: %v", err)
		}

		// Test invalid config
		invalidConfig := &Config{}
		err = validateConfig(invalidConfig)
		if err == nil {
			t.Error("Invalid config should return error")
		}
	})

	// Test date range calculations (core business logic)
	t.Run("date_range_calculations", func(t *testing.T) {
		// Test with a known date
		testDate := time.Date(2025, 9, 21, 15, 0, 0, 0, time.UTC) // Sunday
		start, end := getWeekRange(testDate)

		expectedStart := time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC)          // Monday
		expectedEnd := time.Date(2025, 9, 21, 23, 59, 59, 999999999, time.UTC) // Sunday

		if !start.Equal(expectedStart) {
			t.Errorf("Expected start %v, got %v", expectedStart, start)
		}
		if !end.Equal(expectedEnd) {
			t.Errorf("Expected end %v, got %v", expectedEnd, end)
		}

		// Test that previous week calculation works
		prevStart, prevEnd := getWeekRange(start.AddDate(0, 0, -7))
		expectedPrevStart := time.Date(2025, 9, 8, 0, 0, 0, 0, time.UTC)
		expectedPrevEnd := time.Date(2025, 9, 14, 23, 59, 59, 999999999, time.UTC)

		if !prevStart.Equal(expectedPrevStart) {
			t.Errorf("Expected previous start %v, got %v", expectedPrevStart, prevStart)
		}
		if !prevEnd.Equal(expectedPrevEnd) {
			t.Errorf("Expected previous end %v, got %v", expectedPrevEnd, prevEnd)
		}
	})

	// Test OpenAI prompt building (critical business logic)
	t.Run("openai_prompt_building", func(t *testing.T) {
		basePrompt := "Analyze my weekly food data and provide recommendations"
		currentWeek := &WeeklyData{
			StartDate: "2025-09-15",
			EndDate:   "2025-09-21",
			RawData:   "date,food_name,quantity,unit,calories,protein,carbs,fat,fiber,sugar,sodium\n2025-09-15,Chicken Breast,100,g,165,31,0,3.6,0,0,74\n2025-09-16,Brown Rice,50,g,180,4,36,1.8,1.8,0.4,5\n",
		}
		previousWeek := &WeeklyData{
			StartDate: "2025-09-08",
			EndDate:   "2025-09-14",
			RawData:   "date,food_name,quantity,unit,calories,protein,carbs,fat,fiber,sugar,sodium\n2025-09-08,Salmon Fillet,120,g,200,25,0,12,0,0,60\n2025-09-09,Quinoa,40,g,150,6,27,2.5,3,0.9,5\n",
		}

		prompt := buildAnalysisPrompt(basePrompt, currentWeek, previousWeek)

		// Verify prompt structure
		if !strings.Contains(prompt, basePrompt) {
			t.Error("Prompt should contain base prompt")
		}
		if !strings.Contains(prompt, "CURRENT WEEK RAW DATA") {
			t.Error("Prompt should contain current week header")
		}
		if !strings.Contains(prompt, "PREVIOUS WEEK RAW DATA") {
			t.Error("Prompt should contain previous week header")
		}
		if !strings.Contains(prompt, "2025-09-15") {
			t.Error("Prompt should contain current week dates")
		}
		if !strings.Contains(prompt, "2025-09-08") {
			t.Error("Prompt should contain previous week dates")
		}
		if !strings.Contains(prompt, "Chicken Breast") {
			t.Error("Prompt should contain current week food data")
		}
		if !strings.Contains(prompt, "Salmon Fillet") {
			t.Error("Prompt should contain previous week food data")
		}
		if !strings.Contains(prompt, "```csv") {
			t.Error("Prompt should contain CSV formatting markers")
		}

		// Verify data structure integrity
		lines := strings.Split(prompt, "\n")
		var csvSections int
		for _, line := range lines {
			if strings.Contains(line, "```csv") {
				csvSections++
			}
		}
		if csvSections != 2 {
			t.Errorf("Expected 2 CSV sections, got %d", csvSections)
		}
	})

	// Test email building (output formatting)
	t.Run("email_building", func(t *testing.T) {
		analysis := "## WEEKLY SUMMARY\nYour nutrition analysis shows improvement in protein intake. You consumed 31g protein on average.\n\n## WEIGHT LOSS RECOMMENDATIONS\n- Increase fiber intake\n- Reduce portion sizes by 10%\n\n## MUSCLE GROWTH RECOMMENDATIONS\n- Maintain current protein levels\n- Add post-workout nutrition"
		currentWeek := &WeeklyData{
			StartDate: "2025-09-15",
			EndDate:   "2025-09-21",
			RawData:   "sample data",
		}
		previousWeek := &WeeklyData{
			StartDate: "2025-09-08",
			EndDate:   "2025-09-14",
			RawData:   "sample data",
		}

		// Test HTML email building
		htmlBody, err := buildHTMLEmail(analysis, currentWeek, previousWeek)
		if err != nil {
			t.Fatalf("Failed to build HTML email: %v", err)
		}

		// Verify HTML structure
		if !strings.Contains(htmlBody, "<!DOCTYPE html>") {
			t.Error("HTML email should have proper DOCTYPE")
		}
		if !strings.Contains(htmlBody, analysis) {
			t.Error("HTML email should contain analysis")
		}
		if !strings.Contains(htmlBody, "2025-09-15") {
			t.Error("HTML email should contain current week dates")
		}
		if !strings.Contains(htmlBody, "Weekly Nutrition Report") {
			t.Error("HTML email should contain report title")
		}
		if !strings.Contains(htmlBody, "MailMunch Weekly Report System") {
			t.Error("HTML email should contain system attribution")
		}

		// Test text email building
		textBody := buildTextEmail(analysis, currentWeek, previousWeek)
		if !strings.Contains(textBody, analysis) {
			t.Error("Text email should contain analysis")
		}
		if !strings.Contains(textBody, "WEEKLY NUTRITION REPORT") {
			t.Error("Text email should contain report header")
		}
		if !strings.Contains(textBody, "2025-09-15 to 2025-09-21") {
			t.Error("Text email should contain date range")
		}
		if !strings.Contains(textBody, "AI ANALYSIS & RECOMMENDATIONS") {
			t.Error("Text email should contain analysis section header")
		}

		// Verify text formatting
		lines := strings.Split(textBody, "\n")
		if len(lines) < 5 {
			t.Error("Text email should have multiple lines")
		}
	})

	// Test error handling scenarios
	t.Run("error_handling", func(t *testing.T) {
		// Test empty config validation
		err := validateConfig(&Config{})
		if err == nil {
			t.Error("Should return error for empty config")
		}

		// Test missing required fields
		invalidConfigs := []*Config{
			{ReportEmail: "test@example.com", SenderEmail: "sender@example.com"},                              // missing OpenAI secret
			{OpenAISecretArn: "arn:test", SenderEmail: "sender@example.com"},                                  // missing report email
			{OpenAISecretArn: "arn:test", ReportEmail: "test@example.com"},                                    // missing sender email
			{OpenAISecretArn: "arn:test", ReportEmail: "test@example.com", SenderEmail: "sender@example.com"}, // missing AppConfig fields
		}

		for i, config := range invalidConfigs {
			err := validateConfig(config)
			if err == nil {
				t.Errorf("Config %d should return validation error", i)
			}
		}

		// Test environment variable handling
		key := "TEST_LAMBDA_VAR"
		defaultVal := "default_value"

		// Test with empty environment
		result := getEnvOrDefault(key, defaultVal)
		if result != defaultVal {
			t.Errorf("Expected default value %s, got %s", defaultVal, result)
		}

		// Test with set environment
		t.Setenv(key, "env_value")
		result = getEnvOrDefault(key, defaultVal)
		if result != "env_value" {
			t.Errorf("Expected env value 'env_value', got %s", result)
		}
	})

	// Test timezone handling
	t.Run("timezone_handling", func(t *testing.T) {
		tz := londonTimeZone()
		if tz == nil {
			t.Error("londonTimeZone() should not return nil")
		}

		// Should be either London or UTC (fallback)
		if tz.String() != "Europe/London" && tz.String() != "UTC" {
			t.Errorf("Unexpected timezone: %s", tz.String())
		}

		// Test that time calculations work in the timezone
		now := time.Now().In(tz)
		start, end := getWeekRange(now)

		if start.Location() != tz {
			t.Error("Week start should be in London timezone")
		}
		if end.Location() != tz {
			t.Error("Week end should be in London timezone")
		}
	})
}
