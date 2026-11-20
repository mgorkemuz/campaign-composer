// Package config loads server settings from the environment, with an optional
// .env file for local development. Ported from agents-platform.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Port string
	// DatabaseURL points at PostgreSQL. When empty, the server starts an
	// embedded PostgreSQL under DataDir.
	DatabaseURL string
	DataDir     string
	// AnthropicAPIKey enables the real Claude provider; empty means the mock LLM.
	AnthropicAPIKey string
	AnthropicModel  string
	// LogJSON switches logs to JSON, for log collectors.
	LogJSON bool
}

// DefaultModel is cheap and fast enough for short structured tasks.
const DefaultModel = "claude-haiku-5-5"

// keys lists the only variables the server reads from the .env file.
// Anything else in the file is ignored on purpose.
var keys = []string{
	"PORT", "DATABASE_URL", "DATA_DIR", "LOG_FORMAT",
	"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
}

// Load reads settings from the process environment. Values from envFile are
// used only for variables that are not already set in the environment.
func Load(envFile string) (Config, error) {
	fileVals, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		if v, ok := fileVals[key]; ok && v != "" {
			return v
		}
		return def
	}
	return Config{
		Port:            get("PORT", "8080"),
		DatabaseURL:     get("DATABASE_URL", ""),
		DataDir:         get("DATA_DIR", ".data"),
		AnthropicAPIKey: get("ANTHROPIC_API_KEY", ""),
		AnthropicModel:  get("ANTHROPIC_MODEL", DefaultModel),
		LogJSON:         get("LOG_FORMAT", "text") == "json",
	}, nil
}

// readEnvFile parses KEY=VALUE lines, keeping only the keys the server uses.
// A missing file is not an error.
func readEnvFile(path string) (map[string]string, error) {
	vals := map[string]string{}
	if path == "" {
		return vals, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return vals, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	wanted := map[string]bool{}
	for _, k := range keys {
		wanted[k] = true
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if !wanted[k] {
			continue
		}
		v = envValue(v)
		if strings.Contains(v, "$(") {
			return nil, fmt.Errorf("%s in %s is a shell command, but the file is not run by a shell: put the command's output there", k, path)
		}
		vals[k] = v
	}
	return vals, sc.Err()
}

// envValue reads a value as a shell would: "quoted" or 'quoted' values are
// taken as they are, and an unquoted value ends at " #" (a comment).
func envValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}
