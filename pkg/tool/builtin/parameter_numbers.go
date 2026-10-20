package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func positiveIntParameter(name string, value any) (int, error) {
	const positiveIntegerError = "%s parameter must be a positive integer"
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		if typed < 1 {
			return 0, fmt.Errorf(positiveIntegerError, name)
		}
		return typed, nil
	case int64:
		if typed < 1 || typed > int64(math.MaxInt) {
			return 0, fmt.Errorf(positiveIntegerError, name)
		}
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 1 || parsed > int64(math.MaxInt) {
			return 0, fmt.Errorf(positiveIntegerError, name)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf(positiveIntegerError, name)
	}
	if number < 1 || math.Trunc(number) != number || number >= math.Ldexp(1, strconv.IntSize-1) {
		return 0, fmt.Errorf(positiveIntegerError, name)
	}
	return int(number), nil
}
