package textutil

import "strings"

// NormalizeTitle normalizes title matching for dedupe checks.
func NormalizeTitle(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return ""
	}
	return strings.Join(strings.Fields(lower), " ")
}
