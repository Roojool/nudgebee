package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// MissingRequiredFields returns the list of Required fields absent (or nil-valued)
// from the parsed input, in schema-declared order. Returns nil for tools with no
// Required, or when input isn't JSON-shaped. Used by the executor to route to a
// tool's MissingFieldsResponder BEFORE the generic ValidateToolInput fires, so
// tools with tailored missing-fields messages (e.g. think's narration guard) can
// override the generic "missing required field 'X'" line without changing schema.
func MissingRequiredFields(tool NBTool, input string) []string {
	if tool == nil {
		return nil
	}
	if normalizer, ok := tool.(SchemaValidationInputNormalizer); ok {
		input = normalizer.NormalizeInputForSchemaValidation(input)
	}
	schema := tool.InputSchema()
	if len(schema.Required) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return nil
	}
	var missing []string
	for _, req := range schema.Required {
		val, exists := parsed[req]
		if !exists || val == nil {
			missing = append(missing, req)
			continue
		}
		if s, ok := val.(string); ok && strings.TrimSpace(s) == "" {
			missing = append(missing, req)
		}
	}
	return missing
}

// ParseNBToolCallRequestFromInput reconstructs the NBToolCallRequest shape from
// a raw tool_input string so a MissingFieldsResponder can inspect what the LLM
// actually sent. Mirrors the parsing rules callNbTool uses: a top-level
// "command" string becomes Command; every other top-level key becomes an entry
// in Arguments. Non-JSON input is returned as Command with empty Arguments.
func ParseNBToolCallRequestFromInput(input string) NBToolCallRequest {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "{") {
		return NBToolCallRequest{Command: input}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return NBToolCallRequest{Command: input}
	}
	req := NBToolCallRequest{Arguments: map[string]any{}}
	if cmd, ok := parsed["command"].(string); ok {
		req.Command = cmd
	}
	for k, v := range parsed {
		if k == "command" {
			continue
		}
		req.Arguments[k] = v
	}
	return req
}

// ValidateToolInput checks input against tool.InputSchema, returning nil to pass
// or an LLM-facing error message. Conservative: skips no-schema tools, non-JSON
// input, and unknown keys; on failure lists every error plus the expected schema.
func ValidateToolInput(tool NBTool, input string) *string {
	if tool == nil {
		return nil
	}
	if normalizer, ok := tool.(SchemaValidationInputNormalizer); ok {
		input = normalizer.NormalizeInputForSchemaValidation(input)
	}
	schema := tool.InputSchema()
	if len(schema.Properties) == 0 && len(schema.Required) == 0 && len(schema.RequiredOneOf) == 0 {
		return nil
	}

	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		msg := fmt.Sprintf("Invalid JSON tool input: %s", err.Error())
		return &msg
	}

	errs := collectSchemaErrors(schema, parsed)
	if len(errs) == 0 {
		return nil
	}

	msg := formatSchemaErrorMessage(tool.Name(), schema, errs)
	return &msg
}

func collectSchemaErrors(schema ToolSchema, parsed map[string]any) []string {
	var errs []string

	// Required[]: a key that is missing OR present-with-null both count as
	// "missing" — a null-valued required is functionally as absent as an
	// omitted one, and letting it through would bypass the downstream
	// checkSchemaType (which short-circuits on nil) so a required field
	// could carry a null past validation.
	for _, req := range schema.Required {
		val, exists := parsed[req]
		if !exists {
			errs = append(errs, fmt.Sprintf("missing required field %q", req))
		} else if val == nil {
			errs = append(errs, fmt.Sprintf("required field %q must not be null", req))
		}
	}

	// Each group must have at least one key present with a non-null value.
	for _, group := range schema.RequiredOneOf {
		if !anyKeyPresent(parsed, group) {
			errs = append(errs, fmt.Sprintf("at least one of %v is required", group))
		}
	}

	// Stable order keeps error messages deterministic (tests + LLM prompt cache).
	keys := make([]string, 0, len(parsed))
	for k := range parsed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		val := parsed[key]
		prop, declared := schema.Properties[key]
		if !declared {
			continue
		}
		if typeErr := checkSchemaType(key, val, prop.Type); typeErr != "" {
			errs = append(errs, typeErr)
			// Skip the enum check when the type itself is wrong — the enum
			// error would be a redundant "value X not in enum" line on top
			// of the more informative "expected string, got int" one, and
			// two errors per field is noisier for the LLM's next-turn
			// self-correction.
			continue
		}
		if len(prop.Enum) > 0 && val != nil && !enumContains(prop.Enum, val) {
			errs = append(errs, fmt.Sprintf("field %q value %v not in allowed enum %v", key, val, prop.Enum))
		}
	}
	return errs
}

// anyKeyPresent reports whether any group key exists in parsed with a non-null value.
func anyKeyPresent(parsed map[string]any, group []string) bool {
	for _, key := range group {
		if val, exists := parsed[key]; exists && val != nil {
			return true
		}
	}
	return false
}

func checkSchemaType(key string, val any, schemaType ToolSchemaType) string {
	if val == nil {
		return ""
	}
	switch schemaType {
	case ToolSchemaTypeString:
		if _, ok := val.(string); !ok {
			return fmt.Sprintf("field %q expected string, got %T", key, val)
		}
	case ToolSchemaTypeInteger:
		// JSON numbers parse as float64; accept any numeric but reject fractionals.
		n, ok := numericAsFloat64(val)
		if !ok {
			return fmt.Sprintf("field %q expected integer, got %T", key, val)
		}
		if n != float64(int64(n)) {
			return fmt.Sprintf("field %q expected integer, got non-integer number %v", key, val)
		}
	case ToolSchemaTypeNumber:
		if _, ok := numericAsFloat64(val); !ok {
			return fmt.Sprintf("field %q expected number, got %T", key, val)
		}
	case ToolSchemaTypeBoolean:
		if _, ok := val.(bool); !ok {
			return fmt.Sprintf("field %q expected boolean, got %T", key, val)
		}
	case ToolSchemaTypeArray:
		if _, ok := val.([]any); !ok {
			return fmt.Sprintf("field %q expected array, got %T", key, val)
		}
	case ToolSchemaTypeObject:
		if _, ok := val.(map[string]any); !ok {
			return fmt.Sprintf("field %q expected object, got %T", key, val)
		}
	}
	return ""
}

// numericAsFloat64 returns val as float64 if it's any numeric type (JSON float64,
// every int/uint width, float32, json.Number), else ok=false.
func numericAsFloat64(val any) (float64, bool) {
	switch n := val.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// enumContains reports whether val matches any enum entry: numerics compared as
// float64 (int literal vs JSON float), others via panic-safe reflect.DeepEqual.
func enumContains(enum []any, val any) bool {
	valNum, valIsNum := numericAsFloat64(val)
	for _, e := range enum {
		if valIsNum {
			if eNum, eIsNum := numericAsFloat64(e); eIsNum && eNum == valNum {
				return true
			}
			continue
		}
		if reflect.DeepEqual(e, val) {
			return true
		}
	}
	return false
}

func formatSchemaErrorMessage(toolName string, schema ToolSchema, errs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Invalid tool input for %q:\n", toolName)
	for _, e := range errs {
		fmt.Fprintf(&b, "  - %s\n", e)
	}

	if len(schema.Properties) > 0 {
		fields := make([]string, 0, len(schema.Properties))
		for name, prop := range schema.Properties {
			required := slices.Contains(schema.Required, name)
			marker := ""
			if required {
				marker = " (required)"
			}
			desc := ""
			if prop.Description != "" {
				desc = " — " + prop.Description
			}
			fields = append(fields, fmt.Sprintf("  - %s: %s%s%s", name, prop.Type, marker, desc))
		}
		sort.Strings(fields)
		b.WriteString("\nExpected schema:\n")
		b.WriteString(strings.Join(fields, "\n"))
	}

	if len(schema.RequiredOneOf) > 0 {
		b.WriteString("\nProvide at least one of:")
		for _, group := range schema.RequiredOneOf {
			fmt.Fprintf(&b, "\n  - %s", strings.Join(group, " | "))
		}
	}
	return b.String()
}

// CoerceScalarArrays wraps a bare string into a one-element array for each
// top-level field the schema declares as an array of strings (models often send
// {"node_types":"Workload"}). It returns the repaired input and the changed
// fields (nil when unchanged).
//
//   - Blank strings become [], not [""] (kg_traverse treats [] as "no exclusions").
//   - Strings are never split on commas: CLI commands contain them.
//   - Untouched values keep their exact bytes, and keys are sorted so the
//     output (and the confirmation/cache keys derived from it) is deterministic.
func CoerceScalarArrays(tool NBTool, input string) (string, []string) {
	if tool == nil {
		return input, nil
	}
	// Custom container tool schemas are customer-authored; leave their input alone.
	if _, isCustomContainer := tool.(nbCustomContainerTool); isCustomContainer {
		return input, nil
	}
	schema := tool.InputSchema()
	if len(schema.Properties) == 0 {
		return input, nil
	}
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "{") {
		return input, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return input, nil
	}

	var coerced []string
	for key, val := range raw {
		prop, declared := schema.Properties[key]
		if !declared || prop.Type != ToolSchemaTypeArray || !declaresStringItems(prop) || !coercibleArrayField(key) {
			continue
		}
		widened, ok := widenStringToArray(val)
		if !ok {
			continue
		}
		raw[key] = widened
		coerced = append(coerced, key)
	}
	if len(coerced) == 0 {
		return input, nil
	}
	sort.Strings(coerced)

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(k)
		if err != nil {
			return input, nil
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(raw[k])
	}
	b.WriteByte('}')
	return b.String(), coerced
}

// argvArrayFields hold argv words: wrapping "ls -la" into ["ls -la"] would exec
// a binary named "ls -la", so these keep their type error instead.
var argvArrayFields = map[string]bool{"command": true, "args": true}

func coercibleArrayField(key string) bool { return !argvArrayFields[key] }

// declaresStringItems reports whether an array property declares Items type "string".
func declaresStringItems(prop ToolSchemaProperty) bool {
	t, _ := prop.Items["type"].(string)
	return t == "string"
}

// widenStringToArray returns a JSON string as a one-element array; any other
// value (including null) returns ok=false and is left untouched.
func widenStringToArray(val json.RawMessage) (json.RawMessage, bool) {
	v := bytes.TrimSpace(val)
	if len(v) == 0 || v[0] != '"' {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, false
	}
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("[]"), true
	}

	out := make([]byte, 0, len(v)+2)
	out = append(out, '[')
	out = append(out, v...)
	out = append(out, ']')
	return out, true
}
