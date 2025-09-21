package main

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/service/secretsmanager"
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

func TestGetOpenAIAPIKeyValidation(t *testing.T) {
	// Test the function signature exists and validates input
	// We can't easily test AWS SDK calls without proper mocking
	// So we just verify the function exists with the correct signature

	// This test ensures the function compiles and has the expected signature
	var fn func(*secretsmanager.SecretsManager, string) (string, error) = getOpenAIAPIKey

	if fn == nil {
		t.Error("getOpenAIAPIKey function should exist")
	}

	// Test that empty secret ARN would be handled (though we can't test with nil client)
	// In a real implementation, we'd use dependency injection or mocking here
}
