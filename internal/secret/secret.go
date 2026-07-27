// Package secret resolves a sensitive value from either an environment variable
// or a mounted file. In-cluster the platform seals APIFY_TOKEN and
// ANTHROPIC_API_KEY and mounts them as files under /etc/.secrets — never as env
// — so the value stays out of the process environment (and therefore out of any
// child process the CLI judge spawns). Local dev keeps supplying them via env /
// .env. This helper lets one code path serve both.
package secret

import (
	"os"
	"strings"
)

// Value returns a secret in priority order:
//
//  1. the value env var (e.g. APIFY_TOKEN) — how .env and local dev supply it;
//  2. the file named by the *_FILE env var (e.g. APIFY_TOKEN_FILE), if set;
//  3. defaultFile, if it exists — the in-cluster mount (e.g. /etc/.secrets/apify-token).
//
// File contents are trimmed of surrounding whitespace and the trailing newline a
// SealedSecret's value invariably carries. A value that resolves nowhere is ""; the
// caller decides whether that is fatal.
func Value(valueEnv, fileEnv, defaultFile string) string {
	if v := os.Getenv(valueEnv); v != "" {
		return v
	}
	path := os.Getenv(fileEnv)
	if path == "" {
		path = defaultFile
	}
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
