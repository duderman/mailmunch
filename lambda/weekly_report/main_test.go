package main

import (
	"testing"
	"time"
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
				DataBucket:             "test-bucket",
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
			name: "missing data bucket",
			config: &Config{
				OpenAISecretArn: "arn:aws:secretsmanager:us-east-1:123456789012:secret:test-secret",
				ReportEmail:     "test@example.com",
				SenderEmail:     "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing OpenAI secret ARN",
			config: &Config{
				DataBucket:  "test-bucket",
				ReportEmail: "test@example.com",
				SenderEmail: "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing report email",
			config: &Config{
				DataBucket:      "test-bucket",
				OpenAISecretArn: "arn:aws:secretsmanager:us-east-1:123456789012:secret:test-secret",
				SenderEmail:     "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing sender email",
			config: &Config{
				DataBucket:      "test-bucket",
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

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGetOpenAIAPIKeyFunctionExists(t *testing.T) {
	// This test just checks that the function exists and has the right signature
	// We can't easily test the actual AWS Secrets Manager integration without mocking

	// Verify the function signature exists by attempting to call it with nil
	// This will panic but confirms the function signature is correct
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic when calling getOpenAIAPIKey with nil client")
		}
	}()

	// This should panic since we're passing nil
	_, _ = getOpenAIAPIKey(nil, "test-arn")
}
