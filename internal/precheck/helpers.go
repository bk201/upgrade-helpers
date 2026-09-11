package precheck

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func nestedString(object map[string]any, fields ...string) string {
	value, found, _ := unstructured.NestedString(object, fields...)
	if found {
		return value
	}
	valueAny, found, _ := unstructured.NestedFieldNoCopy(object, fields...)
	if found && valueAny != nil {
		return fmt.Sprint(valueAny)
	}
	return ""
}

func nestedBool(object map[string]any, fields ...string) bool {
	value, found, _ := unstructured.NestedBool(object, fields...)
	if found {
		return value
	}
	return strings.EqualFold(nestedString(object, fields...), "true")
}

func nestedInt(object map[string]any, fields ...string) int64 {
	value, found, _ := unstructured.NestedInt64(object, fields...)
	if found {
		return value
	}
	field, found, _ := unstructured.NestedFieldNoCopy(object, fields...)
	if !found || field == nil {
		return 0
	}
	switch typed := field.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		value, _ := typed.Int64()
		return value
	case string:
		value, _ := strconv.ParseInt(typed, 10, 64)
		return value
	default:
		return 0
	}
}

func nestedNumber(object map[string]any, fields ...string) float64 {
	field, found, _ := unstructured.NestedFieldNoCopy(object, fields...)
	if !found || field == nil {
		return 0
	}
	switch typed := field.(type) {
	case int64:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		value, _ := typed.Float64()
		return value
	case string:
		value, _ := strconv.ParseFloat(typed, 64)
		return value
	default:
		return 0
	}
}

func ceilGiB(value float64) int64 {
	const gib = float64(1024 * 1024 * 1024)
	if value <= 0 {
		return 0
	}
	return int64(math.Ceil(value/gib)) * int64(gib)
}

func namespacedName(object unstructured.Unstructured) string {
	if object.GetNamespace() == "" {
		return object.GetName()
	}
	return object.GetNamespace() + "/" + object.GetName()
}

func objectMap(object map[string]any, fields ...string) map[string]any {
	value, _, _ := unstructured.NestedMap(object, fields...)
	return value
}

func objectSlice(object map[string]any, fields ...string) []any {
	value, _, _ := unstructured.NestedSlice(object, fields...)
	return value
}

func stringSlice(object map[string]any, fields ...string) []string {
	values := objectSlice(object, fields...)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if stringValue, ok := value.(string); ok {
			out = append(out, stringValue)
		}
	}
	return out
}

func settingEffectiveValue(object *unstructured.Unstructured) string {
	if object == nil {
		return ""
	}
	if value := nestedString(object.Object, "value"); value != "" {
		return value
	}
	return nestedString(object.Object, "default")
}
