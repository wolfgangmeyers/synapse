package store

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// RedactedManagedDiagnostic produces the bounded local cause that may cross
// the managed-worker HTTP/UI boundary. It is deliberately run again when a
// durable row is projected because older rows may predate current redaction.
var (
	managedDiagnosticQuotedPath = regexp.MustCompile(`(?i)["'](?:file:(?://)?|~[\\/]|/|[a-z]:[\\/])[^"']*["']`)
	managedDiagnosticFilePath   = regexp.MustCompile(`(?i)file:(?://)?(?:/[^\s"'<>]*|[a-z]:[\\/][^\s"'<>]*)`)
	// A bare path needs a boundary so /host in https://host stays useful.
	managedDiagnosticBarePath = regexp.MustCompile(`(?i)(^|[\s=:(])(?:~[\\/][^\s"'<>]*|/[^\s/"'<>][^\s"'<>]*|[a-z]:[\\/][^\s"'<>]*)`)
)

func RedactedManagedDiagnostic(value string) string {
	// Diagnostics can be copied through HTML-bearing RPC/UI paths. Decode
	// entities before matching so quoted local paths cannot evade redaction.
	value = redactManagedDiagnosticPaths(html.UnescapeString(value))
	fields := strings.Fields(value)
	redactFollowing := 0
	for index, field := range fields {
		lower := strings.ToLower(field)
		if redactFollowing > 0 {
			fields[index] = "[REDACTED]"
			redactFollowing--
			continue
		}
		if parsed, err := url.Parse(field); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			fields[index] = parsed.String()
			continue
		}
		if strings.HasPrefix(lower, "bearer") {
			fields[index] = "[REDACTED]"
			redactFollowing = 1
			continue
		}
		if lower == "authorization:" || lower == "authorization" {
			fields[index] = "[REDACTED]"
			redactFollowing = 2
			continue
		}
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "apikey") || strings.Contains(lower, "cookie") || strings.Contains(lower, "session") || strings.Contains(lower, "process_id") || lower == "pid" || strings.Contains(lower, "runtime_generation") {
			fields[index] = "[REDACTED]"
			if !strings.ContainsAny(field, "=:") {
				redactFollowing = 1
			}
			continue
		}
		if question := strings.IndexByte(field, '?'); question >= 0 {
			fields[index] = field[:question] + "?[REDACTED]"
		}
	}
	result := strings.Join(fields, " ")
	if len(result) > 512 {
		result = result[:512]
	}
	if result == "" {
		return "A local managed-worker failure was recorded"
	}
	return result
}

func redactManagedDiagnosticPaths(value string) string {
	value = managedDiagnosticQuotedPath.ReplaceAllString(value, "[REDACTED]")
	value = managedDiagnosticFilePath.ReplaceAllString(value, "[REDACTED]")
	return managedDiagnosticBarePath.ReplaceAllString(value, "$1[REDACTED]")
}
