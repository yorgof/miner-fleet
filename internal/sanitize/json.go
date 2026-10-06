// Package sanitize removes credentials from telemetry, exports and audit records.
package sanitize

import (
	"encoding/json"
	"strings"
)

func Secret(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.HasSuffix(k, "pass") || strings.Contains(k, "wifipass") || k == "pass" || k == "admin_pass" || k == "wifi_pass" || k == "otp" || strings.Contains(k, "token") || strings.Contains(k, "secret") || strings.Contains(k, "apikey") || strings.Contains(k, "api_key") || strings.Contains(k, "privatekey") || strings.Contains(k, "private_key") || strings.Contains(k, "webhook") || k == "authorization" || k == "cookie"
}

func Value(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			if Secret(k) {
				if _, ok := v.(bool); !ok {
					out[k] = "[redacted]"
					continue
				}
			}
			out[k] = Value(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = Value(v)
		}
		return out
	default:
		return v
	}
}

func JSON(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return "{}"
	}
	b, _ := json.Marshal(Value(v))
	return string(b)
}

// WithoutSecrets omits values entirely so an imported profile cannot send a
// redaction placeholder to a miner as a real password.
func WithoutSecrets(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if !Secret(k) {
				out[k] = WithoutSecrets(v)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = WithoutSecrets(v)
		}
		return out
	default:
		return v
	}
}
