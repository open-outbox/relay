package relay

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

type Server struct {
	storage Storage
	server  *http.Server
	logger  *zap.Logger
}

func registerHandlers(s *Server, mux *http.ServeMux) {

	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/metrics", promhttp.Handler())
}

func NewServer(ctx context.Context, s Storage, addr string, logger *zap.Logger) *Server {

	mux := http.NewServeMux()
	srv := &Server{
		storage: s,
		logger:  logger,
		server: &http.Server{
			Addr:         addr,
			BaseContext:  func(net.Listener) context.Context { return ctx },
			ReadTimeout:  time.Second,
			WriteTimeout: 10 * time.Second,
			Handler:      mux,
		},
	}
	registerHandlers(srv, mux)

	return srv
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// We use the request's context for the DB call
	// stats, err := s.storage.GetStats(r.Context())
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	w.Header().Set("Content-Type", "application/json")
	// Replace the old line with this:
	if err := json.NewEncoder(w).Encode(map[string]string{}); err != nil {
		s.logger.Error("failed to encode stats", zap.Error(err))
	}
}

// Start starts the server. It does NOT block.
func (s *Server) Start(ctx context.Context) (err error) {

	srvErr := make(chan error, 1)
	go func() {
		s.logger.Info("starting relay api", zap.String("addr", s.server.Addr))
		srvErr <- s.server.ListenAndServe()
	}()

	// Wait for interruption.
	select {
	case err = <-srvErr:
		// Error when starting HTTP server.
		return err
	case <-ctx.Done():
		s.logger.Info("Shut down signal received, shudding down api server...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = s.server.Shutdown(shutdownCtx)
	s.logger.Info("Api server stopped")
	return err

}
