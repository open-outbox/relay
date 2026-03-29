package relay

import (
	"context"
	"encoding/json"
	"net/http"

	"go.uber.org/zap"
)

type Server struct {
	storage Storage
	server  *http.Server
	logger  *zap.Logger
}

func NewServer(s Storage, addr string, logger *zap.Logger) *Server {
	mux := http.NewServeMux()
	
	srv := &Server{
		storage: s,
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
		logger: logger,
	}

	mux.HandleFunc("/health", srv.handleHealth)
	return srv
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// We use the request's context for the DB call
	stats, err := s.storage.GetStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// Start starts the server. It does NOT block.
func (s *Server) Start() {
	go func() {
		s.logger.Info("starting health api", zap.String("addr", s.server.Addr))
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Fatal("http server failed", zap.Error(err))
		}
	}()
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("Shutting down Health API...")
	return s.server.Shutdown(ctx)
}