// Package logsafe describes values for log lines without revealing them.
package logsafe

import "fmt"

// Shape describes an arbitrary value for a log line — a task result, a rendered
// workflow output — by its type and size only.
func Shape(v any) string {
	switch t := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return fmt.Sprintf("<string: %d chars>", len(t))
	case []any:
		return fmt.Sprintf("<list: %d items>", len(t))
	case map[string]any:
		return fmt.Sprintf("<map: %d keys>", len(t))
	default:
		return fmt.Sprintf("<%T>", v)
	}
}
