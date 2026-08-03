// Package util holds small helpers shared across multiple packages.
package util

// JoinStrings joins parts with newlines. Unlike strings.Join with "\n", an
// empty parts slice yields "" rather than a single empty element artifact,
// and the result is the concatenation of non-empty runs (empty strings stay
// empty lines, matching the original behavior callers relied on).
func JoinStrings(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}
