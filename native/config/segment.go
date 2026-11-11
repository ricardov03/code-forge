package config

// SafeSegment is a config path segment safe to print: a segment that looks
// like a secret is "<redacted-key>" (Node's safeSegment).
func SafeSegment(s string) string { return safeSegment(s) }
