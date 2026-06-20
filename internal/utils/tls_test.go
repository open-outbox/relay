package utils

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBytes(t *testing.T) {
	// test components
	validPEM := "-----BEGIN CERTIFICATE-----\n" +
		"MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0zg=\n" + // Keep data lines <= 64 chars
		"-----END CERTIFICATE-----"
	malformedPEMHeaderOnly := "-----BEGIN CERTIFICATE-----"
	malformedPEMEmptyPayload := "-----BEGIN CERTIFICATE-----\n\n-----END CERTIFICATE-----"

	rawBytesString := "just-a-plain-string-fallback"
	base64Payload := "base64://" + base64.StdEncoding.EncodeToString([]byte("hello-from-base64"))

	// filepath testing components
	tmpDir := t.TempDir()
	localFilePath := filepath.Join(tmpDir, "open-outbox-test-ca.crt")
	err := os.WriteFile(localFilePath, []byte("file-contents"), 0644)
	if err != nil {
		t.Fatalf("failed to setup mock test file: %v", err)
	}
	validFilePath := "file://" + localFilePath

	// test matrix
	tests := []struct {
		name        string
		input       string
		expected    []byte
		expectError bool
	}{
		{
			name:        "Empty Input",
			input:       "",
			expected:    nil,
			expectError: false,
		},
		{
			name:        "Valid PEM text block",
			input:       validPEM,
			expected:    []byte(validPEM),
			expectError: false,
		},
		{
			name:        "Malformed PEM - Header matched but truncated/no footer",
			input:       malformedPEMHeaderOnly,
			expected:    nil,
			expectError: true,
		},
		{
			name:        "Malformed PEM - Header matched but cryptographic block is empty",
			input:       malformedPEMEmptyPayload,
			expected:    nil,
			expectError: true,
		},
		{
			name:        "Valid File Path resolution",
			input:       validFilePath,
			expected:    []byte("file-contents"),
			expectError: false,
		},
		{
			name:        "Missing File Path resolution (should fail reading)",
			input:       "file://./missing/path/to/certs/ca.crt",
			expected:    nil,
			expectError: true,
		},
		{
			name:        "Valid Base64 String input decoding",
			input:       base64Payload,
			expected:    []byte("hello-from-base64"),
			expectError: false,
		},
		{
			name:        "Plain String Fallback fallback validation",
			input:       rawBytesString,
			expected:    []byte(rawBytesString),
			expectError: false,
		},
	}

	// execute
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := LoadBytes(tt.input)

			// error assertion checking
			if (err != nil) != tt.expectError {
				t.Errorf(
					"LoadBytes() error status = %v, expected error constraint = %v",
					err,
					tt.expectError,
				)
			}

			// content payload checking (if no error was expected)
			if !tt.expectError {
				if string(result) != string(tt.expected) {
					t.Errorf(
						"LoadBytes() returned payload data mismatch.\nGot:  %s\nWant: %s",
						string(result),
						string(tt.expected),
					)
				}
			}
		})
	}
}
