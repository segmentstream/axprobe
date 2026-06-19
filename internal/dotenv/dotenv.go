// Package dotenv loads KEY=VALUE pairs from a .env file into the process
// environment. It is intentionally tiny: real environment variables always win,
// so an exported value overrides the file. A missing file is not an error.
//
// This is how a folder-local secret (e.g. OPENROUTER_API_KEY) is reused across
// runs without ever appearing on the command line or in a manifest.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// Load reads path and sets any variables not already present in the environment.
func Load(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // absent .env is fine
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
