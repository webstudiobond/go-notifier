// Package server provides the Unix domain socket HTTP listener for dispatching notifications.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/ratelimit"
	"github.com/webstudiobond/go-notifier/internal/router"
	"github.com/webstudiobond/go-notifier/internal/sender"
)

func defaultSocketChmod(name string, mode os.FileMode) error {
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	return nil
}

func defaultSocketListen(ctx context.Context, socketPath string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	return ln, nil
}

var (
	socketChmod  = defaultSocketChmod
	socketListen = defaultSocketListen
)

type responsePayload struct {
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Server manages the Unix domain socket HTTP listener and handles incoming dispatch requests.
type Server struct {
	listener   net.Listener
	router     *router.Router
	limiter    *ratelimit.Limiter
	httpServer *http.Server
	socketPath string
	maxMB      int
}

// NewServer initializes a new Server instance.
func NewServer(cfg *config.Config, r *router.Router, l *ratelimit.Limiter) *Server {
	s := &Server{
		router:     r,
		limiter:    l,
		socketPath: cfg.SocketPath,
		maxMB:      cfg.MaxAttachmentSizeMB,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /notify", s.handleNotify)
	mux.HandleFunc("POST /", s.handleNotify)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	return s
}

// ServeHTTP implements http.Handler to allow executing requests against the server pipeline.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpServer.Handler.ServeHTTP(w, r)
}

// ListenAndServe binds the Unix domain socket and begins serving HTTP requests.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o750); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := socketListen(ctx, s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on unix socket %s: %w", s.socketPath, err)
	}
	s.listener = ln

	if err := socketChmod(s.socketPath, 0o600); err != nil {
		if closeErr := ln.Close(); closeErr != nil {
			return fmt.Errorf("%w: close listener: %w", err, closeErr)
		}
		return fmt.Errorf("chmod socket file: %w", err)
	}

	return s.Serve(ln)
}

// Serve begins accepting HTTP requests on the provided listener.
func (s *Server) Serve(ln net.Listener) error {
	s.listener = ln
	if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// Shutdown gracefully terminates the HTTP server and cleans up the Unix socket.
func (s *Server) Shutdown(ctx context.Context) error {
	var errs []error
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown http server: %w", err))
		}
	}
	if s.socketPath != "" {
		if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("cleanup socket file: %w", err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, responsePayload{Status: "ok"})
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if s.limiter != nil && !s.limiter.Allow() {
		writeJSON(w, http.StatusTooManyRequests, responsePayload{Error: "rate limit exceeded"})
		return
	}

	var msg sender.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeJSON(w, http.StatusBadRequest, responsePayload{Error: "invalid request body: " + err.Error()})
		return
	}

	if strings.TrimSpace(msg.Subject) == "" &&
		strings.TrimSpace(msg.BodyText) == "" &&
		strings.TrimSpace(msg.BodyHTML) == "" {
		writeJSON(w, http.StatusBadRequest, responsePayload{Error: "empty notification: subject or body is required"})
		return
	}

	if s.maxMB > 0 && len(msg.Attachments) > 0 {
		maxBytes := s.maxMB * 1024 * 1024
		for _, att := range msg.Attachments {
			decodedLen := base64.StdEncoding.DecodedLen(len(att.ContentBase64))
			if decodedLen > maxBytes {
				writeJSON(w, http.StatusRequestEntityTooLarge, responsePayload{
					Error: fmt.Sprintf("attachment %s exceeds size limit (%d MB)", att.Filename, s.maxMB),
				})
				return
			}
		}
	}

	if err := s.router.Dispatch(r.Context(), &msg); err != nil {
		if errors.Is(err, router.ErrMissingSMTPRecipients) {
			writeJSON(w, http.StatusBadRequest, responsePayload{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, responsePayload{Error: "dispatch failed: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, responsePayload{Status: "delivered"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}
