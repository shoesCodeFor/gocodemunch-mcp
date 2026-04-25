package security

import "context"

// PathGuard validates filesystem scope and traversal policy.
type PathGuard interface {
	ValidatePath(ctx context.Context, path string) error
}

// RegexGuard validates regex safety policy.
type RegexGuard interface {
	ValidatePattern(ctx context.Context, pattern string) error
}

// SecretFilter masks or strips sensitive strings from outbound payloads.
type SecretFilter interface {
	Filter(ctx context.Context, content string) string
}
