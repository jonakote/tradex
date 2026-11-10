package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvSetsVariables(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := `# comment
KEY_ONE=value1
KEY_TWO="quoted value"
KEY_THREE='single quoted'
  KEY_FOUR = spaced  

EMPTY_VALUE=
`
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Clear any existing values
	os.Unsetenv("KEY_ONE")
	os.Unsetenv("KEY_TWO")
	os.Unsetenv("KEY_THREE")
	os.Unsetenv("KEY_FOUR")
	os.Unsetenv("EMPTY_VALUE")

	if err := LoadDotEnv(envFile); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key      string
		expected string
	}{
		{"KEY_ONE", "value1"},
		{"KEY_TWO", "quoted value"},
		{"KEY_THREE", "single quoted"},
		{"KEY_FOUR", "spaced"},
		{"EMPTY_VALUE", ""},
	}
	for _, tt := range tests {
		got := os.Getenv(tt.key)
		if got != tt.expected {
			t.Errorf("%s = %q, want %q", tt.key, got, tt.expected)
		}
	}
}

func TestLoadDotEnvDoesNotOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("MY_VAR=from_file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("MY_VAR", "from_env")
	defer os.Unsetenv("MY_VAR")

	if err := LoadDotEnv(envFile); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("MY_VAR"); got != "from_env" {
		t.Fatalf("MY_VAR = %q, want 'from_env' (should not override)", got)
	}
}

func TestLoadDotEnvMissingFileIsNotError(t *testing.T) {
	if err := LoadDotEnv("/nonexistent/.env"); err != nil {
		t.Fatalf("missing .env should not error, got: %v", err)
	}
}

func TestLoadDotEnvSkipsInvalidLines(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := `no_equals_sign
=no_key
 =also_no_key
VALID_KEY=valid_value
`
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("VALID_KEY")
	if err := LoadDotEnv(envFile); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("VALID_KEY"); got != "valid_value" {
		t.Fatalf("VALID_KEY = %q, want 'valid_value'", got)
	}
}

func TestParseDotEnvLine(t *testing.T) {
	tests := []struct {
		line  string
		key   string
		value string
		ok    bool
	}{
		{"KEY=value", "KEY", "value", true},
		{"KEY=\"value\"", "KEY", "value", true},
		{"KEY='value'", "KEY", "value", true},
		{"KEY=", "KEY", "", true},
		{"=value", "", "", false},
		{"no_equals", "", "", false},
		{" SPACED_KEY = spaced_value ", "SPACED_KEY", "spaced_value", true},
	}
	for _, tt := range tests {
		key, value, ok := parseDotEnvLine(tt.line)
		if ok != tt.ok || key != tt.key || value != tt.value {
			t.Errorf("parseDotEnvLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.line, key, value, ok, tt.key, tt.value, tt.ok)
		}
	}
}
