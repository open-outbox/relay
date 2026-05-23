package utils

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var pemHeaderRegex = regexp.MustCompile(`(?m)^-----BEGIN [A-Z ]+-----`)

// LoadBytes converts a configuration string into raw bytes.
// It supports:
// - Explicit file paths prefixed with "file://" (e.g., "file:///etc/certs/ca.crt")
// - Explicit Base64 encoded data prefixed with "base64://" (e.g., "base64://TUlJQk...")
// - Inline, un-prefixed raw PEM text blocks
// - Plain un-prefixed text fallback (treated as raw bytes)
func LoadBytes(input string) ([]byte, error) {
	if input == "" {
		return nil, nil
	}

	//TODO: Do we need to check for "Insecure File Operations"
	if strings.HasPrefix(input, "file://") {
		filePath := strings.TrimPrefix(input, "file://")
		if filePath == "" {
			return nil, fmt.Errorf("malformed input: 'file://' prefix provided but path is empty")
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("configured file path could not be loaded: %w", err)
		}
		return data, nil
	}

	if strings.HasPrefix(input, "base64://") {
		b64Data := strings.TrimPrefix(input, "base64://")
		data, err := base64.StdEncoding.DecodeString(b64Data)
		if err != nil {
			return nil, fmt.Errorf(
				"malformed input: 'base64://' prefix provided but decoding failed: %w",
				err,
			)
		}
		return data, nil
	}

	if pemHeaderRegex.MatchString(input) {
		block, _ := pem.Decode([]byte(input))
		if block == nil {
			return nil, fmt.Errorf(
				"malformed PEM data: header matched but parsing failed (check for syntax or truncation errors)",
			)
		}
		if len(block.Bytes) == 0 {
			return nil, fmt.Errorf(
				"malformed PEM data: header matched but internal cryptographic payload is empty or corrupted",
			)
		}
		return []byte(input), nil
	}

	return []byte(input), nil
}
