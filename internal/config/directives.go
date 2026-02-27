package config

import (
	"fmt"
	"strings"
)

// ParseDirective parses one redis/sentinel config line with shell-like quoting.
func ParseDirective(line string) ([]string, error) {
	var (
		result  []string
		current strings.Builder
		quote   rune
		escape  bool
		started bool
	)

	for _, r := range line {
		switch {
		case escape:
			current.WriteRune(r)
			escape = false
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			if quote == '"' && r == '\\' {
				escape = true
				continue
			}
			current.WriteRune(r)
			started = true
		case r == '\\':
			escape = true
			started = true
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				result = append(result, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}

	if escape {
		return nil, fmt.Errorf("unterminated escape in directive: %q", line)
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in directive: %q", line)
	}
	if started {
		result = append(result, current.String())
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty directive after parsing: %q", line)
	}
	return result, nil
}

// NormalizeUserLines strips comments and empty lines.
func NormalizeUserLines(lines []string) []string {
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

// IsReservedRedisDirective reports whether a directive is managed by operator.
func IsReservedRedisDirective(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	key := strings.ToLower(tokens[0])
	if strings.HasPrefix(key, "cluster-announce-") {
		return true
	}
	reserved := map[string]struct{}{
		"cluster-enabled":                 {},
		"cluster-config-file":             {},
		"cluster-preferred-endpoint-type": {},
		"replicaof":                       {},
		"port":                            {},
		"tls-port":                        {},
		"dir":                             {},
	}
	_, ok := reserved[key]
	return ok
}

// IsReservedSentinelDirective reports whether a directive is managed by operator.
func IsReservedSentinelDirective(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	key := strings.ToLower(tokens[0])
	if key == "port" || key == "tls-port" {
		return true
	}
	if key != "sentinel" || len(tokens) < 2 {
		return false
	}
	secondary := strings.ToLower(tokens[1])
	switch secondary {
	case "monitor", "auth-pass", "resolve-hostnames", "announce-hostnames":
		return true
	default:
		return false
	}
}

// ValidateUserDirectives validates user directives with reserved-key guards.
func ValidateUserDirectives(lines []string, reservedChecker func([]string) bool) ([]string, error) {
	normalized := NormalizeUserLines(lines)
	result := make([]string, 0, len(normalized))
	for _, line := range normalized {
		tokens, err := ParseDirective(line)
		if err != nil {
			return nil, err
		}
		if reservedChecker(tokens) {
			return nil, fmt.Errorf("directive %q is reserved by operator", strings.ToLower(strings.Join(tokens, " ")))
		}
		result = append(result, line)
	}
	return result, nil
}
