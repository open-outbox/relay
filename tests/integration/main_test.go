//go:build integration

package integration

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPostgres.stop()
	sharedKafka.stop()
	os.Exit(code)
}
