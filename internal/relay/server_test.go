package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

func TestServer_Handlers(t *testing.T) {
	logger := zap.NewNop()

	t.Run("Stats_Success", func(t *testing.T) {
		mockStorage := new(MockStorage)
		mockStorage.On("GetStats", mock.Anything).Return(Stats{
			PendingCount: 10,
			OldestAgeSec: 10,
		}, nil)

		srv := NewServer(context.Background(), mockStorage, new(MockPublisher), ":8080", logger)

		req := httptest.NewRequest(http.MethodGet, "/stats", nil)
		rr := httptest.NewRecorder()

		srv.server.Handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), `"pending_count":10`)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Readyz_Full_Success", func(t *testing.T) {
		mockStorage := new(MockStorage)
		mockPublisher := new(MockPublisher)

		mockStorage.On("Ping", mock.Anything).Return(nil)
		mockPublisher.On("Ping", mock.Anything).Return(nil)

		srv := NewServer(context.Background(), mockStorage, mockPublisher, ":8080", logger)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rr := httptest.NewRecorder()

		srv.server.Handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		mockStorage.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
	})

	t.Run("Readyz_Storage_Failure", func(t *testing.T) {
		mockStorage := new(MockStorage)
		mockStorage.On("Ping", mock.Anything).Return(errors.New("db down"))

		srv := NewServer(context.Background(), mockStorage, new(MockPublisher), ":8080", logger)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rr := httptest.NewRecorder()

		srv.server.Handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
		assert.Contains(t, rr.Body.String(), "storage error")
	})
}

func TestServer_StartAndStop(t *testing.T) {
	// This test hits the graceful shutdown and Start logic
	ctx, cancel := context.WithCancel(context.Background())
	srv := NewServer(ctx, &MockStorage{}, &MockPublisher{}, "127.0.0.1:0", zap.NewNop())

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	// Give it a moment to start
	cancel()

	err := <-done
	assert.NoError(t, err, "Server should shut down gracefully without error")
}
