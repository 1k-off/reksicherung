package evidence

import "regexp"

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret|authorization)\b(\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s;,]+)`)
	bearerPattern           = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/-]+=*`)
	urlCredentialsPattern   = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	privateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	awsAccessKeyPattern     = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	gitHubTokenPattern      = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)
	jwtPattern              = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
)

// Redact replaces common inline credential forms with [REDACTED]. Call it on
// unstructured tool output and error strings before considering them for
// evidence. It is intentionally conservative and cannot identify every secret
// encoded as an arbitrary-looking value; Event validation separately restricts
// metadata keys and rejects recognizable credential patterns.
func Redact(value string) string {
	value = privateKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = urlCredentialsPattern.ReplaceAllString(value, "${1}[REDACTED]@")
	value = bearerPattern.ReplaceAllString(value, "${1}[REDACTED]")
	value = secretAssignmentPattern.ReplaceAllString(value, "${1}${2}[REDACTED]")
	value = awsAccessKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = gitHubTokenPattern.ReplaceAllString(value, "[REDACTED]")
	return jwtPattern.ReplaceAllString(value, "[REDACTED]")
}

func looksSecretBearing(value string) bool {
	return Redact(value) != value
}
