package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupOption(t *testing.T) {
	options := map[string]int{"": 1, "one": 1, "two": 2}

	for in, want := range map[string]int{"": 1, "one": 1, " TWO\t": 2} {
		got, err := lookupOption("test option", in, options)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	_, err := lookupOption("test option", "three", options)
	require.EqualError(t, err, "unsupported test option: three (supported: one, two)")
}
