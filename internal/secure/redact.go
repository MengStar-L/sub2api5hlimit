package secure

import (
	"regexp"
	"strings"
)

var secretPattern = regexp.MustCompile(`(?i)(admin-[A-Za-z0-9_-]+|sk-[A-Za-z0-9_-]{8,}|"?(?:api_key|password|key)"?\s*[:=]\s*"?)[^"\s,}]+`)

func Redact(value string) string {
	return secretPattern.ReplaceAllString(value, "[REDACTED]")
}

func MaskAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	prefix := ""
	body := value
	if strings.HasPrefix(value, "sk-") {
		prefix = "sk-"
		body = strings.TrimPrefix(value, prefix)
	}
	if len(body) < 4 {
		return prefix + "…"
	}
	return prefix + "…" + body[len(body)-4:]
}
