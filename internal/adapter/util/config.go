package util

// LookupString returns the first string value found for any of the given keys
// in the config map. Viper lowercases all keys, so callers typically pass both
// camelCase and lowercase variants.
func LookupString(m map[string]interface{}, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v, true
		}
	}
	return "", false
}

// StringVal returns the value of the first matching key found in m.
func StringVal(m map[string]interface{}, keys ...string) string {
	v, _ := LookupString(m, keys...)
	return v
}

// LookupBool returns the first bool value found for any of the given keys.
// The second return value distinguishes an explicit false from an unset key.
func LookupBool(m map[string]interface{}, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k].(bool); ok {
			return v, true
		}
	}
	return false, false
}

// LookupStringMap returns a map[string]string by coercing any string values
// found under any of the given keys.
func LookupStringMap(m map[string]interface{}, keys ...string) map[string]string {
	for _, k := range keys {
		if raw, ok := m[k].(map[string]interface{}); ok {
			out := make(map[string]string, len(raw))
			for mk, mv := range raw {
				if s, ok := mv.(string); ok {
					out[mk] = s
				}
			}
			return out
		}
	}
	return nil
}
