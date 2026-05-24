package controller

import (
	"reflect"
	"testing"
)

func TestApplyTemplate(t *testing.T) {
	configs := map[string]string{
		"HOST": "localhost",
		"PORT": "5432",
		"USER": "admin",
	}

	tests := []struct {
		name        string
		templateStr string
		expected    string
		expectErr   bool
	}{
		{
			name:        "valid template",
			templateStr: "postgres://{{get \"USER\"}}@{{get \"HOST\"}}:{{get \"PORT\"}}/db",
			expected:    "postgres://admin@localhost:5432/db",
			expectErr:   false,
		},
		{
			name:        "missing key returns empty",
			templateStr: "hello {{get \"MISSING\"}}",
			expected:    "hello ",
			expectErr:   false,
		},
		{
			name:        "invalid template syntax",
			templateStr: "postgres://{{get \"USER\"", // Missing closing braces
			expected:    "",
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := applyTemplate(configs, tt.templateStr)
			if (err != nil) != tt.expectErr {
				t.Errorf("applyTemplate() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if val != tt.expected {
				t.Errorf("applyTemplate() = %v, want %v", val, tt.expected)
			}
		})
	}
}

func TestApplyJoin(t *testing.T) {
	configs := map[string]string{
		"APP_URL_1": "http://node1",
		"APP_URL_2": "http://node2",
		"APP_URL_3": "http://node3",
		"OTHER_KEY": "ignore_me",
	}

	tests := []struct {
		name          string
		sourceKeys    []string
		sourcePattern string
		separator     string
		expected      string
		expectErr     bool
	}{
		{
			name:          "join explicit source keys",
			sourceKeys:    []string{"APP_URL_1", "APP_URL_3"},
			sourcePattern: "",
			separator:     ",",
			expected:      "http://node1,http://node3",
			expectErr:     false,
		},
		{
			name:          "join missing explicit source key",
			sourceKeys:    []string{"APP_URL_1", "MISSING_KEY"},
			sourcePattern: "",
			separator:     ",",
			expected:      "http://node1",
			expectErr:     false,
		},
		{
			name:          "join regex pattern",
			sourceKeys:    nil,
			sourcePattern: "^APP_URL_\\d+$",
			separator:     ";",
			expected:      "http://node1;http://node2;http://node3",
			expectErr:     false,
		},
		{
			name:          "join invalid regex pattern",
			sourceKeys:    nil,
			sourcePattern: "^APP_URL_[unclosed",
			separator:     ";",
			expected:      "",
			expectErr:     true,
		},
		{
			name:          "join no keys and no pattern",
			sourceKeys:    nil,
			sourcePattern: "",
			separator:     ",",
			expected:      "",
			expectErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := applyJoin(configs, tt.sourceKeys, tt.sourcePattern, tt.separator)
			if (err != nil) != tt.expectErr {
				t.Errorf("applyJoin() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if val != tt.expected {
				t.Errorf("applyJoin() = %v, want %v", val, tt.expected)
			}
		})
	}
}

func TestApplyBase64(t *testing.T) {
	configs := map[string]string{
		"PLAIN":   "hello world",
		"ENCODED": "aGVsbG8gd29ybGQ=",
		"INVALID": "!!not base64!!",
	}

	tests := []struct {
		name      string
		sourceKey string
		operation string
		expected  string
		expectErr bool
	}{
		{
			name:      "encode string",
			sourceKey: "PLAIN",
			operation: "encode",
			expected:  "aGVsbG8gd29ybGQ=",
			expectErr: false,
		},
		{
			name:      "decode string",
			sourceKey: "ENCODED",
			operation: "decode",
			expected:  "hello world",
			expectErr: false,
		},
		{
			name:      "decode invalid string",
			sourceKey: "INVALID",
			operation: "decode",
			expected:  "",
			expectErr: true,
		},
		{
			name:      "missing key",
			sourceKey: "MISSING",
			operation: "encode",
			expected:  "",
			expectErr: true,
		},
		{
			name:      "default to encode",
			sourceKey: "PLAIN",
			operation: "",
			expected:  "aGVsbG8gd29ybGQ=",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := applyBase64(configs, tt.sourceKey, tt.operation)
			if (err != nil) != tt.expectErr {
				t.Errorf("applyBase64() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if val != tt.expected {
				t.Errorf("applyBase64() = %v, want %v", val, tt.expected)
			}
		})
	}
}

func TestApplyJsonPath(t *testing.T) {
	configs := map[string]string{
		"JSON_DATA": `{"user":{"name":"alice","age":30,"active":true},"tags":["go","kubernetes"]}`,
		"INVALID":   `{malformed_json}`,
	}

	tests := []struct {
		name      string
		sourceKey string
		path      string
		expected  string
		expectErr bool
	}{
		{
			name:      "extract string primitive",
			sourceKey: "JSON_DATA",
			path:      "$.user.name",
			expected:  "alice",
			expectErr: false,
		},
		{
			name:      "extract integer primitive",
			sourceKey: "JSON_DATA",
			path:      "user.age",
			expected:  "30",
			expectErr: false,
		},
		{
			name:      "extract boolean primitive",
			sourceKey: "JSON_DATA",
			path:      "user.active",
			expected:  "true",
			expectErr: false,
		},
		{
			name:      "extract from array index using brackets",
			sourceKey: "JSON_DATA",
			path:      "tags[1]",
			expected:  "kubernetes",
			expectErr: false,
		},
		{
			name:      "extract nested object as json string",
			sourceKey: "JSON_DATA",
			path:      "user",
			expected:  `{"active":true,"age":30,"name":"alice"}`,
			expectErr: false,
		},
		{
			name:      "extract array out of bounds",
			sourceKey: "JSON_DATA",
			path:      "tags[5]",
			expected:  "",
			expectErr: true,
		},
		{
			name:      "extract missing key",
			sourceKey: "JSON_DATA",
			path:      "user.missing",
			expected:  "",
			expectErr: true,
		},
		{
			name:      "invalid json source",
			sourceKey: "INVALID",
			path:      "any.path",
			expected:  "",
			expectErr: true,
		},
		{
			name:      "missing source key",
			sourceKey: "MISSING",
			path:      "any.path",
			expected:  "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := applyJsonPath(configs, tt.sourceKey, tt.path)
			if (err != nil) != tt.expectErr {
				t.Errorf("applyJsonPath() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if val != tt.expected {
				t.Errorf("applyJsonPath() = %v, want %v", val, tt.expected)
			}
		})
	}
}

func TestProcessTransforms(t *testing.T) {
	configs := map[string]string{
		"DB_HOST":  "localhost",
		"DB_PORT":  "5432",
		"DB_USER":  "admin",
		"RAW_JSON": `{"settings":{"enable_tls":true}}`,
	}

	transforms := []TransformSpec{
		{
			Name:     "DATABASE_URL",
			Type:     "template",
			Template: "postgres://{{get \"DB_USER\"}}@{{get \"DB_HOST\"}}:{{get \"DB_PORT\"}}/db",
		},
		{
			Name:      "TLS_ENABLED",
			Type:      "jsonpath",
			SourceKey: "RAW_JSON",
			JsonPath:  "settings.enable_tls",
		},
		{
			Name:      "B64_USER",
			Type:      "base64",
			SourceKey: "DB_USER",
			Operation: "encode",
		},
		{
			Name:       "JOINED",
			Type:       "join",
			SourceKeys: []string{"DB_USER", "DB_PORT"},
			Separator:  "-",
		},
	}

	result, err := ProcessTransforms(configs, transforms)
	if err != nil {
		t.Fatalf("ProcessTransforms failed: %v", err)
	}

	expected := map[string]string{
		"DB_HOST":      "localhost",
		"DB_PORT":      "5432",
		"DB_USER":      "admin",
		"RAW_JSON":     `{"settings":{"enable_tls":true}}`,
		"DATABASE_URL": "postgres://admin@localhost:5432/db",
		"TLS_ENABLED":  "true",
		"B64_USER":     "YWRtaW4=", // "admin" -> base64
		"JOINED":       "admin-5432",
	}

	if !reflect.DeepEqual(result, expected) {
		t.Errorf("ProcessTransforms() mismatch.\nGot:  %v\nWant: %v", result, expected)
	}

	// Test error propagation
	invalidTransforms := []TransformSpec{
		{
			Name: "BAD",
			Type: "unsupported",
		},
	}

	_, err = ProcessTransforms(configs, invalidTransforms)
	if err == nil {
		t.Errorf("ProcessTransforms() with invalid type should fail")
	}
}
