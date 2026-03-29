package relay

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

type Server struct {
	storage Storage
	server  *http.Server
}

func NewServer(s Storage, addr string) *Server {
	mux := http.NewServeMux()
	
	srv := &Server{
		storage: s,
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
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
		log.Printf("Health API listening on %s", s.server.Addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	log.Println("Shutting down Health API...")
	return s.server.Shutdown(ctx)
}