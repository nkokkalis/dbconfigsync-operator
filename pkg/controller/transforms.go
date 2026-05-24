package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// ProcessTransforms executes the list of transforms on the fetched configuration map.
// It modifies the configuration map in-place (or returns a copy) with the generated keys.
func ProcessTransforms(configs map[string]string, transforms []TransformSpec) (map[string]string, error) {
	// Create a copy of the configuration map to avoid side effects
	result := make(map[string]string)
	for k, v := range configs {
		result[k] = v
	}

	for _, t := range transforms {
		if t.Name == "" {
			continue
		}

		switch strings.ToLower(t.Type) {
		case "template":
			val, err := applyTemplate(result, t.Template)
			if err != nil {
				return nil, fmt.Errorf("failed to apply template transform for key '%s': %w", t.Name, err)
			}
			result[t.Name] = val

		case "join":
			val, err := applyJoin(result, t.SourceKeys, t.SourcePattern, t.Separator)
			if err != nil {
				return nil, fmt.Errorf("failed to apply join transform for key '%s': %w", t.Name, err)
			}
			result[t.Name] = val

		case "base64":
			val, err := applyBase64(result, t.SourceKey, t.Operation)
			if err != nil {
				return nil, fmt.Errorf("failed to apply base64 transform for key '%s': %w", t.Name, err)
			}
			result[t.Name] = val

		case "jsonpath":
			val, err := applyJsonPath(result, t.SourceKey, t.JsonPath)
			if err != nil {
				return nil, fmt.Errorf("failed to apply jsonpath transform for key '%s': %w", t.Name, err)
			}
			result[t.Name] = val

		default:
			return nil, fmt.Errorf("unsupported transform type '%s' for key '%s'", t.Type, t.Name)
		}
	}

	return result, nil
}

// applyTemplate executes a Go text/template with the configs map as context.
func applyTemplate(configs map[string]string, templateStr string) (string, error) {
	// Custom template function helper to allow indexing map keys that might have special characters
	funcMap := template.FuncMap{
		"get": func(key string) string {
			return configs[key]
		},
	}

	tmpl, err := template.New("config").Funcs(funcMap).Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	// Execute template with the configs map as context
	if err := tmpl.Execute(&buf, configs); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// applyJoin joins configuration values using a separator.
func applyJoin(configs map[string]string, sourceKeys []string, sourcePattern string, separator string) (string, error) {
	var vals []string

	// 1. Join explicit keys list
	if len(sourceKeys) > 0 {
		for _, key := range sourceKeys {
			if val, ok := configs[key]; ok {
				vals = append(vals, val)
			}
		}
	} else if sourcePattern != "" {
		// 2. Join dynamically matched keys by regular expression
		re, err := regexp.Compile(sourcePattern)
		if err != nil {
			return "", fmt.Errorf("invalid regular expression '%s': %w", sourcePattern, err)
		}

		// Find matching keys
		var matchedKeys []string
		for key := range configs {
			if re.MatchString(key) {
				matchedKeys = append(matchedKeys, key)
			}
		}

		// Sort keys alphabetically to ensure deterministic join order
		sort.Strings(matchedKeys)

		// Collect values
		for _, key := range matchedKeys {
			vals = append(vals, configs[key])
		}
	} else {
		return "", fmt.Errorf("join transform must specify either 'sourceKeys' or 'sourcePattern'")
	}

	return strings.Join(vals, separator), nil
}

// applyBase64 handles encoding or decoding of config values.
func applyBase64(configs map[string]string, sourceKey string, operation string) (string, error) {
	srcVal, ok := configs[sourceKey]
	if !ok {
		return "", fmt.Errorf("source key '%s' not found in configs", sourceKey)
	}

	if strings.ToLower(operation) == "decode" {
		decoded, err := base64.StdEncoding.DecodeString(srcVal)
		if err != nil {
			return "", fmt.Errorf("failed to decode base64 string: %w", err)
		}
		return string(decoded), nil
	}

	// Default is encode
	return base64.StdEncoding.EncodeToString([]byte(srcVal)), nil
}

// applyJsonPath navigates a JSON string using basic dotted/bracketed path navigation.
func applyJsonPath(configs map[string]string, sourceKey string, path string) (string, error) {
	srcVal, ok := configs[sourceKey]
	if !ok {
		return "", fmt.Errorf("source key '%s' not found in configs", sourceKey)
	}

	var data interface{}
	if err := json.Unmarshal([]byte(srcVal), &data); err != nil {
		return "", fmt.Errorf("failed to parse JSON from source key '%s': %w", sourceKey, err)
	}

	// Normalize bracket syntax: users[0] -> users.0
	normalizedPath := strings.ReplaceAll(path, "[", ".")
	normalizedPath = strings.ReplaceAll(normalizedPath, "]", "")
	normalizedPath = strings.TrimPrefix(normalizedPath, "$.")
	parts := strings.Split(normalizedPath, ".")

	curr := data
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch v := curr.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return "", fmt.Errorf("key '%s' not found in JSON map", part)
			}
			curr = val
		case []interface{}:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
				return "", fmt.Errorf("cannot index array with non-integer '%s'", part)
			}
			if idx < 0 || idx >= len(v) {
				return "", fmt.Errorf("array index %d out of bounds (length %d)", idx, len(v))
			}
			curr = v[idx]
		default:
			return "", fmt.Errorf("value at part '%s' is not traversable (actual type: %T)", part, curr)
		}
	}

	// Format result
	if curr == nil {
		return "", nil
	}
	switch v := curr.(type) {
	case string:
		return v, nil
	case bool, float64:
		return fmt.Sprintf("%v", v), nil
	default:
		// Fallback to json marshal for object/array
		bytes, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("failed to marshal resulting JSON subelement: %w", err)
		}
		return string(bytes), nil
	}
}
