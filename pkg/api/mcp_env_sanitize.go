package api

import "strings"

func omitEmptySensitiveMCPEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return env
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		if isSensitiveMCPEnvKey(key) && strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isSensitiveMCPEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "PASSWD", "PWD", "AUTH"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
