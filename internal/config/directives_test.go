package config

import "testing"

func TestParseDirectiveQuoted(t *testing.T) {
	tokens, err := ParseDirective(`rename-command FLUSHALL ""`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}
	if tokens[2] != "" {
		t.Fatalf("expected empty third token, got %q", tokens[2])
	}
}

func TestValidateUserDirectivesRejectReserved(t *testing.T) {
	_, err := ValidateUserDirectives([]string{"cluster-enabled yes"}, IsReservedRedisDirective)
	if err == nil {
		t.Fatalf("expected reserved directive error")
	}
}

func TestValidateUserDirectivesRejectReservedTLSDirective(t *testing.T) {
	_, err := ValidateUserDirectives([]string{"tls-port 6379"}, IsReservedRedisDirective)
	if err == nil {
		t.Fatalf("expected reserved tls directive error")
	}
}

func TestValidateUserDirectivesAllowsMultiToken(t *testing.T) {
	lines, err := ValidateUserDirectives([]string{"client-output-buffer-limit normal 0 0 0"}, IsReservedRedisDirective)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

func TestValidateUserSentinelDirectivesRejectReservedTLSDirective(t *testing.T) {
	_, err := ValidateUserDirectives([]string{"tls-auth-clients no"}, IsReservedSentinelDirective)
	if err == nil {
		t.Fatalf("expected reserved sentinel tls directive error")
	}
}
