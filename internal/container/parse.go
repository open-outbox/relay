package container

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// lookupOption maps a configuration value to its typed option, ignoring case
// and surrounding whitespace. An empty key in options acts as the default.
func lookupOption[T any](name, value string, options map[string]T) (T, error) {
	if option, ok := options[strings.ToLower(strings.TrimSpace(value))]; ok {
		return option, nil
	}

	supported := slices.DeleteFunc(slices.Sorted(maps.Keys(options)), func(key string) bool {
		return key == ""
	})
	var zero T
	return zero, fmt.Errorf(
		"unsupported %s: %s (supported: %s)",
		name, value, strings.Join(supported, ", "),
	)
}
