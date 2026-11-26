package relay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvent_HeaderMap(t *testing.T) {
	tests := []struct {
		name    string
		headers string
		want    map[string]string
	}{
		{name: "empty", headers: "", want: nil},
		{name: "JSON null", headers: "null", want: nil},
		{name: "empty object", headers: "{}", want: map[string]string{}},
		{
			name:    "strings are used as they are",
			headers: `{"trace_id":"abc","content-type":"application/json"}`,
			want:    map[string]string{"trace_id": "abc", "content-type": "application/json"},
		},
		{
			name:    "escaped strings are decoded",
			headers: `{"quote":"say \"hi\"","unicode":"café"}`,
			want:    map[string]string{"quote": `say "hi"`, "unicode": "café"},
		},
		{
			name:    "numbers and booleans become their JSON text",
			headers: `{"retry":3,"ratio":0.25,"big":12345678901234567890,"sampled":true,"debug":false}`,
			want: map[string]string{
				"retry": "3", "ratio": "0.25", "big": "12345678901234567890",
				"sampled": "true", "debug": "false",
			},
		},
		{
			name:    "null values are left out",
			headers: `{"keep":"x","drop":null}`,
			want:    map[string]string{"keep": "x"},
		},
		{
			name:    "whitespace around the object is ignored",
			headers: " \n{ \"a\" : 1 }\n",
			want:    map[string]string{"a": "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Event{Headers: json.RawMessage(tt.headers)}.HeaderMap()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvent_HeaderMap_Invalid(t *testing.T) {
	tests := map[string]struct {
		headers     string
		errContains string
	}{
		"nested object":  {`{"user":{"id":1}}`, `header "user": nested objects and arrays`},
		"array":          {`{"tags":["a","b"]}`, `header "tags": nested objects and arrays`},
		"top-level list": {`["a"]`, "headers must be a JSON object"},
		"top-level text": {`"a"`, "headers must be a JSON object"},
		"malformed":      {`{"a":`, "headers must be a JSON object"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Event{Headers: json.RawMessage(tt.headers)}.HeaderMap()

			var pubErr *PublishError
			require.ErrorAs(t, err, &pubErr)
			assert.False(t, pubErr.IsRetryable, "the same headers can never succeed")
			assert.Equal(t, "INVALID_HEADERS", pubErr.Code)
			assert.Contains(t, pubErr.Error(), tt.errContains)
		})
	}
}

func BenchmarkEvent_HeaderMap(b *testing.B) {
	event := Event{Headers: json.RawMessage(
		`{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","content-type":"application/json",` +
			`"tenant":"acme","retry":3,"sampled":true}`,
	)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := event.HeaderMap(); err != nil {
			b.Fatal(err)
		}
	}
}
