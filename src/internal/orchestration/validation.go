package orchestration

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var boolTrue = map[string]struct{}{
	"true": {}, "1": {}, "yes": {}, "on": {},
}

var boolFalse = map[string]struct{}{
	"false": {}, "0": {}, "no": {}, "off": {},
}

// CoerceArguments applies schema-guided string coercion for boolean/integer/number values.
func CoerceArguments(arguments map[string]any, schema map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}

	props := schemaProperties(schema)
	out := make(map[string]any, len(arguments))
	for key, value := range arguments {
		property, ok := props[key]
		if !ok {
			out[key] = value
			continue
		}
		typeName := schemaString(property, "type")
		coerced, changed := coerceValue(value, typeName)
		if changed {
			out[key] = coerced
			continue
		}
		out[key] = value
	}
	return out
}

// ValidateArguments performs baseline required-field and primitive-type checks.
func ValidateArguments(arguments map[string]any, schema map[string]any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}

	for _, required := range schemaStrings(schema, "required") {
		value, ok := arguments[required]
		if !ok || value == nil {
			return fmt.Errorf("%q is required", required)
		}
	}

	props := schemaProperties(schema)
	for key, value := range arguments {
		property, ok := props[key]
		if !ok {
			continue
		}
		typeName := schemaString(property, "type")
		if typeName == "" {
			continue
		}
		if !matchesType(value, typeName) {
			return fmt.Errorf("%q must be %s", key, typeName)
		}
		if err := validateEnumConstraint(key, value, property); err != nil {
			return err
		}
	}

	return nil
}

func coerceValue(value any, typeName string) (any, bool) {
	raw, ok := value.(string)
	if !ok {
		return value, false
	}

	switch typeName {
	case "boolean":
		key := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := boolTrue[key]; ok {
			return true, true
		}
		if _, ok := boolFalse[key]; ok {
			return false, true
		}
	case "integer":
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			return parsed, true
		}
	case "number":
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			return parsed, true
		}
	}

	return value, false
}

func matchesType(value any, typeName string) bool {
	switch typeName {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			// JSON numbers decode to float64; allow integral values.
			return float64(int64(value.(float64))) == value.(float64)
		default:
			return false
		}
	case "number":
		switch value.(type) {
		case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		default:
			return false
		}
	case "array":
		switch value.(type) {
		case []any, []string:
			return true
		default:
			return false
		}
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}

func schemaProperties(schema map[string]any) map[string]map[string]any {
	propertiesRaw, ok := schema["properties"].(map[string]any)
	if !ok {
		return map[string]map[string]any{}
	}

	out := make(map[string]map[string]any, len(propertiesRaw))
	for key, value := range propertiesRaw {
		property, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out[key] = property
	}

	return out
}

func schemaString(schema map[string]any, key string) string {
	value, _ := schema[key].(string)
	return value
}

func schemaStrings(schema map[string]any, key string) []string {
	items, ok := schema[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if ok {
			out = append(out, text)
		}
	}
	return out
}

func validateEnumConstraint(key string, value any, property map[string]any) error {
	enumValues, ok := property["enum"].([]any)
	if !ok || len(enumValues) == 0 {
		return nil
	}

	for _, enumValue := range enumValues {
		if enumMatches(value, enumValue) {
			return nil
		}
	}

	return fmt.Errorf("%q must be one of %s", key, formatEnumValues(enumValues))
}

func enumMatches(value, enumValue any) bool {
	if reflect.DeepEqual(value, enumValue) {
		return true
	}

	valueFloat, valueIsNumber := toFloat64(value)
	enumFloat, enumIsNumber := toFloat64(enumValue)
	if valueIsNumber && enumIsNumber {
		return valueFloat == enumFloat
	}

	return false
}

func toFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func formatEnumValues(values []any) string {
	items := make([]string, 0, len(values))
	for _, value := range values {
		items = append(items, fmt.Sprintf("%v", value))
	}
	return fmt.Sprintf("[%s]", strings.Join(items, ", "))
}
