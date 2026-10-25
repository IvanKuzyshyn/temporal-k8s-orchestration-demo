package plan

import (
	"fmt"
	"sort"
	"strings"
)

// ParseDependencies trims and deduplicates a comma-separated dependency list.
// A wholly blank annotation is a root; empty elements in a nonblank list are errors.
func ParseDependencies(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	for _, entry := range strings.Split(raw, ",") {
		name := strings.TrimSpace(entry)
		if !validServiceName(name) {
			return nil, fmt.Errorf("invalid dependency entry %q", name)
		}
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
