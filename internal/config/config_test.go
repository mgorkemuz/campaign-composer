package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	for _, k := range keys {
		t.Setenv(k, "")
	}
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.AnthropicModel != DefaultModel || cfg.AnthropicAPIKey != "" || cfg.LogJSON {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFileAndEnvPrecedence(t *testing.T) {
	for _, k := range keys {
		t.Setenv(k, "")
	}
	path := writeEnv(t, `# comment
export ANTHROPIC_MODEL="claude-opus-5-5"
ANTHROPIC_API_KEY=test-key # trailing comment
PORT=9000
UNRELATED_SECRET=ignored
`)
	t.Setenv("PORT", "7000") // the real environment wins over the file

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicModel != "claude-opus-5-5" {
		t.Errorf("model = %q", cfg.AnthropicModel)
	}
	if cfg.AnthropicAPIKey != "test-key" {
		t.Errorf("key = %q, want the value without the comment", cfg.AnthropicAPIKey)
	}
	if cfg.Port != "7000" {
		t.Errorf("port = %q, want env to override the file", cfg.Port)
	}
}

func TestLoadRejectsShellCommands(t *testing.T) {
	for _, k := range keys {
		t.Setenv(k, "")
	}
	if _, err := Load(writeEnv(t, "ANTHROPIC_API_KEY=$(cat key.txt)\n")); err == nil {
		t.Fatal("want an error for a $() value")
	}
}
