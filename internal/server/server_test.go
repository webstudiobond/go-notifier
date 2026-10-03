package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/ratelimit"
	"github.com/webstudiobond/go-notifier/internal/router"
	"github.com/webstudiobond/go-notifier/internal/sender"
)

type mockSender struct {
	sendFunc func(ctx context.Context, msg *sender.Message, sendAttachments bool) error
	name     string
}

func (m *mockSender) Name() string {
	return m.name
}

func (m *mockSender) Send(ctx context.Context, msg *sender.Message, sendAttachments bool) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, msg, sendAttachments)
	}
	return nil
}

type memoryListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemoryListener() *memoryListener {
	return &memoryListener{
		conns:  make(chan net.Conn, 16),
		closed: make(chan struct{}),
	}
}

func (m *memoryListener) Accept() (net.Conn, error) {
	select {
	case conn := <-m.conns:
		return conn, nil
	case <-m.closed:
		return nil, net.ErrClosed
	}
}

func (m *memoryListener) Close() error {
	m.once.Do(func() {
		close(m.closed)
	})
	return nil
}

func (m *memoryListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "memory", Net: "unix"}
}

func (m *memoryListener) Dial() (net.Conn, error) {
	select {
	case <-m.closed:
		return nil, net.ErrClosed
	default:
	}
	serverConn, clientConn := net.Pipe()
	m.conns <- serverConn
	return clientConn, nil
}

func TestServer_HandleHealthz(t *testing.T) {
	cfg := &config.Config{
		SocketPath: "/tmp/test.sock",
	}
	s := NewServer(cfg, nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", http.NoBody)
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res responsePayload
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode healthz response: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("expected status ok, got %s", res.Status)
	}
}

func TestServer_HandleNotify(t *testing.T) {
	oversizedData := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), 2*1024*1024))
	validData := base64.StdEncoding.EncodeToString([]byte("sample attachment"))

	tests := []struct {
		mockErr        error
		name           string
		body           string
		expectedStatus int
		maxMB          int
		rateLimit      int
		useRootPath    bool
	}{
		{
			name:           "valid notify dispatch to slash endpoint",
			useRootPath:    true,
			body:           `{"to":["admin@alpha.example"],"subject":"Test","body_text":"Hello"}`,
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid notify dispatch to notify endpoint",
			useRootPath:    false,
			body:           `{"to":["admin@beta.example"],"subject":"Alert","body_text":"Notice"}`,
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid json payload returns bad request",
			useRootPath:    false,
			body:           `{not valid json}`,
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty notification body and subject returns bad request",
			useRootPath:    false,
			body:           `{"to":[],"subject":"","body_text":""}`,
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "valid notify without recipients succeeds for non-smtp channel",
			useRootPath:    false,
			body:           `{"subject":"Push Alert","body_text":"Notice"}`,
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "attachment exceeding size limit returns request entity too large",
			useRootPath:    false,
			body:           `{"to":["admin@gamma.example"],"subject":"Big File","attachments":[{"filename":"big.bin","content_base64":"` + oversizedData + `"}]}`,
			maxMB:          1,
			rateLimit:      10,
			expectedStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:           "attachment within size limit succeeds",
			useRootPath:    false,
			body:           `{"to":["admin@delta.example"],"subject":"Small File","attachments":[{"filename":"ok.txt","content_base64":"` + validData + `"}]}`,
			maxMB:          5,
			rateLimit:      10,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "rate limit exceeded returns too many requests",
			useRootPath:    false,
			body:           `{"to":["admin@rate.example"],"subject":"Rate Limit Test"}`,
			maxMB:          10,
			rateLimit:      0,
			expectedStatus: http.StatusTooManyRequests,
		},
		{
			name:           "downstream dispatch error returns bad gateway",
			useRootPath:    false,
			body:           `{"to":["admin@fail.example"],"subject":"Dispatch Error"}`,
			mockErr:        errors.New("connection refused"),
			maxMB:          10,
			rateLimit:      10,
			expectedStatus: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channelName := "mock-channel"
			mSender := &mockSender{
				name: channelName,
				sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
					return tt.mockErr
				},
			}

			cfg := &config.Config{
				SocketPath:          "/tmp/test.sock",
				MaxAttachmentSizeMB: tt.maxMB,
				DefaultChannels:     []string{channelName},
				AdminEmails: []string{
					"admin@alpha.example",
					"admin@beta.example",
					"admin@gamma.example",
					"admin@delta.example",
					"admin@rate.example",
					"admin@fail.example",
				},
			}

			r := router.NewRouter(cfg, map[string]sender.Sender{channelName: mSender})

			var lim *ratelimit.Limiter
			if tt.rateLimit > 0 {
				lim = ratelimit.NewLimiter(tt.rateLimit, tt.rateLimit)
			} else {
				lim = ratelimit.NewLimiter(1, 1)
				if !lim.Allow() {
					t.Fatal("expected first token allowed")
				}
			}

			srv := NewServer(cfg, r, lim)

			endpoint := "/notify"
			if tt.useRootPath {
				endpoint = "/"
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d (body: %s)", tt.expectedStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestServer_ListenerLifecycle(t *testing.T) {
	chName := "lifecycle-ch"
	cfg := &config.Config{
		SocketPath:          "/tmp/test-lifecycle.sock",
		MaxAttachmentSizeMB: 5,
		DefaultChannels:     []string{chName},
	}

	mSender := &mockSender{name: chName}
	r := router.NewRouter(cfg, map[string]sender.Sender{chName: mSender})
	lim := ratelimit.NewLimiter(10, 10)

	srv := NewServer(cfg, r, lim)
	memLn := newMemoryListener()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(memLn)
	}()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return memLn.Dial()
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix/healthz", http.NoBody)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatalf("close body: %v", closeErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if sErr := srv.Shutdown(shutdownCtx); sErr != nil {
		t.Fatalf("shutdown error: %v", sErr)
	}
	if closeErr := memLn.Close(); closeErr != nil {
		t.Fatalf("close mem listener: %v", closeErr)
	}

	err = <-serverErr
	if err != nil {
		t.Fatalf("server returned error: %v", err)
	}
}

func TestServer_Notify_WithoutTo(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		errContains    string
		channels       []string
		expectedStatus int
		expectAdmin    bool
	}{
		{
			name:           "multi_channel_with_smtp_and_telegram_delivers_to_telegram",
			channels:       []string{"smtp", "telegram"},
			body:           `{"subject":"Deployment Alert","body_text":"Build succeeded"}`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "smtp_only_without_to_returns_bad_request",
			channels:       []string{"smtp"},
			body:           `{"subject":"Mail alert","body_text":"Missing recipient"}`,
			expectedStatus: http.StatusBadRequest,
			expectAdmin:    true,
			errContains:    "recipient list (to) is required for SMTP delivery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			senders := make(map[string]sender.Sender, len(tt.channels))
			for _, ch := range tt.channels {
				chName := ch
				senders[chName] = &mockSender{
					name: chName,
					sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
						return nil
					},
				}
			}

			cfg := &config.Config{
				DefaultChannels:     tt.channels,
				SMTPEnabled:         true,
				AdminFilterRequired: tt.expectAdmin,
			}

			r := router.NewRouter(cfg, senders)
			lim := ratelimit.NewLimiter(10, 10)
			srv := NewServer(cfg, r, lim)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/notify", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d (body: %s)", tt.expectedStatus, rec.Code, rec.Body.String())
			}
			if tt.errContains != "" {
				var res responsePayload
				if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
					t.Fatalf("decode response error: %v", err)
				}
				if !strings.Contains(res.Error, tt.errContains) {
					t.Errorf("unexpected error message: %s", res.Error)
				}
			}
		})
	}
}

func TestServer_Notify_CompletelyEmpty_BadRequest(t *testing.T) {
	cfg := &config.Config{
		DefaultChannels: []string{"mock-ch"},
	}
	r := router.NewRouter(cfg, nil)
	lim := ratelimit.NewLimiter(10, 10)
	srv := NewServer(cfg, r, lim)

	tests := []struct {
		name string
		body string
	}{
		{"empty_json", `{}`},
		{"empty_fields", `{"subject":"","body_text":"","body_html":""}`},
		{"whitespace_fields", `{"subject":"  ","body_text":"\n\t","body_html":"   "}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/notify", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
			}
			var res responsePayload
			if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
				t.Fatalf("decode response error: %v", err)
			}
			if res.Error != "empty notification: subject or body is required" {
				t.Errorf("expected empty notification error, got %s", res.Error)
			}
		})
	}
}

var mockServerForChmod *Server

func mockFailingChmod(_ string, _ os.FileMode) error {
	return errors.New("simulated chmod error")
}

func mockChmodWithPreclosedListener(_ string, _ os.FileMode) error {
	if mockServerForChmod != nil && mockServerForChmod.listener != nil {
		if err := mockServerForChmod.listener.Close(); err != nil {
			return err
		}
	}
	return errors.New("simulated chmod error")
}

func mockFailingListen(_ context.Context, _ string) (net.Listener, error) {
	return nil, errors.New("simulated listen error")
}

type failingAcceptListener struct {
	net.Listener
}

func (f *failingAcceptListener) Accept() (net.Conn, error) {
	return nil, errors.New("simulated accept error")
}

func (f *failingAcceptListener) Close() error {
	return nil
}

func (f *failingAcceptListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "failing", Net: "unix"}
}

type failingResponseWriter struct {
	header http.Header
}

func (f *failingResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *failingResponseWriter) WriteHeader(_ int) {}

func (f *failingResponseWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("simulated write error")
}

func TestServer_ListenAndServe_Success(t *testing.T) {
	sockPath := filepath.Join(".", "test_live_success.sock")
	defer func() {
		if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Logf("cleanup socket error: %v", err)
		}
	}()

	cfg := &config.Config{
		SocketPath: sockPath,
	}
	srv := NewServer(cfg, nil, nil)

	ctx := t.Context()

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.ListenAndServe(ctx)
	}()

	var ready bool
	for range 50 {
		if fi, err := os.Stat(sockPath); err == nil {
			if fi.Mode()&os.ModeSocket != 0 {
				ready = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("socket file was not created")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	if err := <-errChan; err != nil {
		t.Fatalf("ListenAndServe failed: %v", err)
	}
}

func TestServer_ListenAndServe_Errors(t *testing.T) {
	origChmod := socketChmod
	origListen := socketListen
	defer func() {
		socketChmod = origChmod
		socketListen = origListen
		mockServerForChmod = nil
	}()

	notADirFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(notADirFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	staleDir := filepath.Join(t.TempDir(), "staledir")
	if err := os.Mkdir(staleDir, 0o750); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to write child: %v", err)
	}

	shortSockPath := filepath.Join(".", "test_err.sock")
	defer func() {
		if err := os.Remove(shortSockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Logf("cleanup socket error: %v", err)
		}
	}()

	tests := []struct {
		setup      func(srv *Server)
		name       string
		socketPath string
		wantErr    string
		cancelCtx  bool
	}{
		{
			name:       "create_socket_directory_error",
			socketPath: filepath.Join(notADirFile, "sock.sock"),
			wantErr:    "create socket directory",
		},
		{
			name:       "remove_stale_socket_error",
			socketPath: staleDir,
			wantErr:    "remove stale socket",
		},
		{
			name:       "listen_on_unix_socket_error",
			socketPath: shortSockPath,
			setup: func(_ *Server) {
				socketListen = mockFailingListen
			},
			wantErr: "listen on unix socket",
		},
		{
			name:       "chmod_error_clean_close",
			socketPath: shortSockPath,
			setup: func(_ *Server) {
				socketChmod = mockFailingChmod
			},
			wantErr: "chmod socket file: simulated chmod error",
		},
		{
			name:       "chmod_error_failing_close",
			socketPath: shortSockPath,
			setup: func(srv *Server) {
				mockServerForChmod = srv
				socketChmod = mockChmodWithPreclosedListener
			},
			wantErr: "close listener",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketChmod = origChmod
			socketListen = origListen
			mockServerForChmod = nil

			srv := NewServer(&config.Config{SocketPath: tt.socketPath}, nil, nil)
			if tt.setup != nil {
				tt.setup(srv)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelCtx {
				cancel()
			}

			err := srv.ListenAndServe(ctx)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestServer_Serve_Error(t *testing.T) {
	srv := NewServer(&config.Config{SocketPath: "/tmp/dummy.sock"}, nil, nil)
	fln := &failingAcceptListener{}

	err := srv.Serve(fln)
	if err == nil || !strings.Contains(err.Error(), "server error: simulated accept error") {
		t.Fatalf("expected server error, got %v", err)
	}
}

func startActiveServer(t *testing.T, srv *Server) func() {
	t.Helper()
	memLn := newMemoryListener()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(memLn)
	}()

	clientConn, err := memLn.Dial()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	if _, err := clientConn.Write([]byte("POST /notify HTTP/1.1\r\nHost: unix\r\nContent-Length: 100\r\n\r\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	return func() {
		if err := clientConn.Close(); err != nil {
			t.Logf("close clientConn: %v", err)
		}
		if err := memLn.Close(); err != nil {
			t.Logf("close memLn: %v", err)
		}
		if err := <-serverErr; err != nil {
			t.Logf("server returned error: %v", err)
		}
	}
}

func TestServer_Shutdown_Branches(t *testing.T) {
	staleDir := filepath.Join(t.TempDir(), "staledir")
	if err := os.Mkdir(staleDir, 0o750); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to write child: %v", err)
	}

	tests := []struct {
		setup         func(srv *Server) func()
		name          string
		socketPath    string
		wantErr       string
		wantSecondErr string
		hasTimeout    bool
		useRawServer  bool
	}{
		{
			name:         "nil_http_server_and_empty_socket",
			useRawServer: true,
		},
		{
			name: "shutdown_http_server_error",
			setup: func(srv *Server) func() {
				return startActiveServer(t, srv)
			},
			hasTimeout: true,
			wantErr:    "shutdown http server",
		},
		{
			name:         "cleanup_socket_file_error",
			socketPath:   staleDir,
			useRawServer: true,
			wantErr:      "cleanup socket file",
		},
		{
			name:       "both_errors_joined",
			socketPath: staleDir,
			setup: func(srv *Server) func() {
				return startActiveServer(t, srv)
			},
			hasTimeout:    true,
			wantErr:       "shutdown http server",
			wantSecondErr: "cleanup socket file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *Server
			if tt.useRawServer {
				srv = &Server{socketPath: tt.socketPath}
			} else {
				srv = NewServer(&config.Config{SocketPath: tt.socketPath}, nil, nil)
			}

			if tt.setup != nil {
				cleanup := tt.setup(srv)
				defer cleanup()
			}

			ctx := context.Background()
			if tt.hasTimeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
				defer cancel()
			}

			err := srv.Shutdown(ctx)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
			if tt.wantSecondErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantSecondErr)) {
				t.Fatalf("expected second error containing %q, got %v", tt.wantSecondErr, err)
			}
		})
	}
}

func TestServer_WriteJSON_Error(t *testing.T) {
	fw := &failingResponseWriter{}
	writeJSON(fw, http.StatusOK, responsePayload{Status: "ok"})
	if fw.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type header to be set")
	}
}

func TestDefaultSocketChmod_Error(t *testing.T) {
	err := defaultSocketChmod(filepath.Join(t.TempDir(), "nonexistent", "sock.sock"), 0o600)
	if err == nil {
		t.Fatalf("expected error from chmod on non-existent path")
	}
}

func TestDefaultSocketListen(t *testing.T) {
	sockPath := filepath.Join(".", "test_default_listen.sock")
	defer func() {
		if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Logf("cleanup socket error: %v", err)
		}
	}()

	ln, err := defaultSocketListen(t.Context(), sockPath)
	if err != nil {
		t.Fatalf("expected successful default listen: %v", err)
	}
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("expected clean close: %v", closeErr)
	}
}

func TestDefaultSocketListen_Error(t *testing.T) {
	_, err := defaultSocketListen(t.Context(), filepath.Join(t.TempDir(), "nonexistent", "sock.sock"))
	if err == nil {
		t.Fatalf("expected error from listen on non-existent directory")
	}
}
