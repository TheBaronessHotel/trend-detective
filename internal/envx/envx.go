// Package envx reads service settings from environment variables.
package envx

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Required returns the named variables, or one error listing every one that's
// missing.
func Required(names ...string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	var missing []string
	for _, n := range names {
		v := os.Getenv(n)
		if v == "" {
			missing = append(missing, n)
		}
		out[n] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// String returns the variable, or fallback when it's unset.
func String(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// Int returns the variable as a positive integer, or fallback when it's unset.
func Int(name string, fallback int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, v)
	}
	return n, nil
}

// Duration returns the variable parsed by time.ParseDuration, like "90s", or
// fallback when it's unset.
func Duration(name string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration like 90s, got %q", name, v)
	}
	return d, nil
}
