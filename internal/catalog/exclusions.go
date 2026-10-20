package catalog

import "strings"

// excludedModel matches the entire upstream ID, ignoring case and surrounding
// pattern whitespace. Only '*' is special; it matches any sequence, including
// slashes and the empty string. Other characters are literal.
func excludedModel(id string, patterns []string) bool {
	id = strings.ToLower(id)
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		parts := strings.Split(pattern, "*")
		if len(parts) == 1 {
			if id == pattern {
				return true
			}
			continue
		}
		if !strings.HasPrefix(id, parts[0]) {
			continue
		}
		rest := id[len(parts[0]):]
		matched := true
		for _, part := range parts[1 : len(parts)-1] {
			i := strings.Index(rest, part)
			if i < 0 {
				matched = false
				break
			}
			rest = rest[i+len(part):]
		}
		if matched && strings.HasSuffix(rest, parts[len(parts)-1]) {
			return true
		}
	}
	return false
}
