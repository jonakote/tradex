package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads a .env file and sets any variables that are not already
// present in the environment. This ensures real environment variables take
// precedence over .env values.
//
// The format supports:
//   - KEY=VALUE
//   - KEY="VALUE" or KEY='VALUE' (quotes stripped)
//   - Lines starting with # are comments
//   - Empty lines are skipped
//   - Leading/trailing whitespace is trimmed from keys and values
//
// It does NOT support variable interpolation, multi-line values, or export prefixes.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No .env file is not an error
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := parseDotEnvLine(line)
		if !ok {
			continue
		}
		// Only set if not already in environment
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func parseDotEnvLine(line string) (key, value string, ok bool) {
	eqIdx := strings.IndexByte(line, '=')
	if eqIdx < 1 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eqIdx])
	value = strings.TrimSpace(line[eqIdx+1:])

	// Strip surrounding quotes
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	// Reject keys with spaces or empty keys
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}

	return key, value, true
}
