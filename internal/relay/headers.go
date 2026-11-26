package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// HeaderMap decodes the event's headers into the string key/value pairs
// that brokers carry. Headers must be a flat JSON object:
//
//   - strings are used as they are;
//   - numbers and booleans become their JSON text ("3", "true");
//   - null values are left out;
//   - nested objects and arrays are rejected.
//
// Empty headers and a JSON null return a nil map. Any error is a
// non-retryable *PublishError with the code INVALID_HEADERS, since
// retrying the same event can never fix it.
func (e Event) HeaderMap() (map[string]string, error) {
	raw := bytes.TrimSpace(e.Headers)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, InvalidHeadersError(fmt.Errorf("headers must be a JSON object: %w", err))
	}

	headers := make(map[string]string, len(fields))
	for key, value := range fields {
		switch value[0] {
		case '"':
			s, err := jsonString(value)
			if err != nil {
				return nil, InvalidHeadersError(fmt.Errorf("header %q: %w", key, err))
			}
			headers[key] = s
		case 'n':
			// null: nothing to send.
		case '{', '[':
			return nil, InvalidHeadersError(fmt.Errorf(
				"header %q: nested objects and arrays are not supported, "+
					"values must be strings, numbers or booleans", key,
			))
		default:
			// A number, true or false: their JSON text is the header value.
			headers[key] = string(value)
		}
	}
	return headers, nil
}

// jsonString decodes a JSON string literal. Most header values contain no
// escapes, so the quotes are simply stripped.
func jsonString(value json.RawMessage) (string, error) {
	if bytes.IndexByte(value, '\\') < 0 {
		return string(value[1 : len(value)-1]), nil
	}
	var s string
	err := json.Unmarshal(value, &s)
	return s, err
}

// InvalidHeadersError wraps err as the non-retryable INVALID_HEADERS
// publish error, for header problems a publisher finds itself.
func InvalidHeadersError(err error) *PublishError {
	return &PublishError{Err: err, IsRetryable: false, Code: "INVALID_HEADERS"}
}
