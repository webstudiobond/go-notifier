package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/ratelimit"
	"github.com/webstudiobond/go-notifier/internal/router"
)

var (
	testExited   bool
	testExitCode int
)

func mockExit(code int) {
	testExited = true
	testExitCode = code
}

func mockServerClosedFactory(_ *config.Config, _ *router.Router, _ *ratelimit.Limiter) serverRunner {
	return &mockServer{listenErr: http.ErrServerClosed}
}

type mockServer struct {
	listenHook   func()
	started      chan struct{}
	shutdownDone chan struct{}
	listenErr    error
	shutdownErr  error
}

func (m *mockServer) ListenAndServe(_ context.Context) error {
	if m.started != nil {
		close(m.started)
	}
	if m.listenHook != nil {
		m.listenHook()
	}
	if m.shutdownDone != nil {
		<-m.shutdownDone
	}
	return m.listenErr
}

func (m *mockServer) Shutdown(_ context.Context) error {
	if m.shutdownDone != nil {
		close(m.shutdownDone)
	}
	return m.shutdownErr
}

type testSecretFile struct {
	name string
	data string
}

func writeSecretFiles(t *testing.T, dir string, files []testSecretFile) {
	t.Helper()
	for _, f := range files {
		target := filepath.Clean(filepath.Join(dir, f.name))
		if err := os.WriteFile(target, []byte(f.data), 0o600); err != nil {
			t.Fatalf("failed to write secret file %s: %v", f.name, err)
		}
	}
}

func setupEnvDirs(t *testing.T, mandatoryDir, optionalDir string) {
	t.Helper()
	envPairs := []struct {
		key string
		val string
	}{
		{"MANDATORY_SECRETS_DIR", mandatoryDir},
		{"OPTIONAL_SECRETS_DIR", optionalDir},
	}
	for _, p := range envPairs {
		t.Setenv(p.key, p.val)
	}
}

func setupAllSecrets(t *testing.T) (mandatoryDir, optionalDir string) {
	t.Helper()
	mandatoryDir = t.TempDir()
	optionalDir = t.TempDir()

	mandatoryFiles := []testSecretFile{
		{"smtp_host", "mail.example.com"},
		{"smtp_port", "465"},
		{"smtp_mail", "noreply@example.com"},
		{"smtp_password", "initialpass1"},
	}
	writeSecretFiles(t, mandatoryDir, mandatoryFiles)

	optionalFiles := []testSecretFile{
		{"telegram_bot_token.txt", "123456789:ABCdefGhIjkLmNoPqRsTuVwXyZ"},
		{"telegram_chat_id.txt", "-1001234567890"},
		{"matrix_url.txt", "https://matrix.example.com"},
		{"matrix_room_id.txt", "!roomid:example.com"},
		{"matrix_access_token.txt", "matrixtoken"},
		{"ntfy_url.txt", "https://ntfy.example.org"},
		{"ntfy_topic.txt", "alerts"},
		{"ntfy_token.txt", "ntfysecret"},
		{"admin_emails.txt", "admin@example.com"},
	}
	writeSecretFiles(t, optionalDir, optionalFiles)

	return mandatoryDir, optionalDir
}

func setupTelegramOnlySecrets(t *testing.T) (mandatoryDir, optionalDir string) {
	t.Helper()
	mandatoryDir = t.TempDir()
	optionalDir = t.TempDir()

	optionalFiles := []testSecretFile{
		{"telegram_bot_token.txt", "987654321:XYZdefGhIjkLmNoPqRsTuVwXyZ"},
		{"telegram_chat_id.txt", "-1009876543210"},
	}
	writeSecretFiles(t, optionalDir, optionalFiles)

	return mandatoryDir, optionalDir
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		if _, copyErr := io.Copy(&buf, r); copyErr != nil {
			t.Errorf("io.Copy failed: %v", copyErr)
		}
		outC <- buf.String()
	}()

	fn()

	if closeErr := w.Close(); closeErr != nil {
		t.Errorf("w.Close failed: %v", closeErr)
	}
	os.Stdout = oldStdout
	return <-outC
}

func TestMain(t *testing.T) {
	origExit := osExit
	origNewServer := newServer
	defer func() {
		osExit = origExit
		newServer = origNewServer
	}()

	tests := []struct {
		setup      func(t *testing.T)
		name       string
		wantCode   int
		wantErrLog bool
		wantExit   bool
	}{
		{
			name:       "daemon configuration error triggers exit 1",
			wantErrLog: true,
			wantExit:   true,
			wantCode:   1,
			setup: func(t *testing.T) {
				setupEnvDirs(t, t.TempDir(), t.TempDir())
			},
		},
		{
			name:       "daemon runs and exits cleanly without error",
			wantErrLog: false,
			wantExit:   false,
			wantCode:   0,
			setup: func(t *testing.T) {
				mDir, oDir := setupAllSecrets(t)
				setupEnvDirs(t, mDir, oDir)
				newServer = mockServerClosedFactory
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testExited = false
			testExitCode = 0
			osExit = mockExit

			tt.setup(t)

			out := captureStdout(t, func() {
				main()
			})

			if testExited != tt.wantExit {
				t.Fatalf("expected exited=%v, got %v", tt.wantExit, testExited)
			}
			if tt.wantExit && testExitCode != tt.wantCode {
				t.Fatalf("expected exit code %d, got %d", tt.wantCode, testExitCode)
			}
			if tt.wantErrLog && !strings.Contains(out, "daemon terminated with error") {
				t.Fatalf("expected error log in output, got %s", out)
			}
		})
	}
}

func TestRun(t *testing.T) {
	origNewServer := newServer
	defer func() {
		newServer = origNewServer
	}()

	tests := []struct {
		setup     func(t *testing.T)
		name      string
		errSubstr string
		wantErr   bool
	}{
		{
			name: "default directories configuration load failure",
			setup: func(_ *testing.T) {
			},
			wantErr:   true,
			errSubstr: "load configuration",
		},
		{
			name: "custom directories with empty directory",
			setup: func(t *testing.T) {
				setupEnvDirs(t, t.TempDir(), t.TempDir())
			},
			wantErr:   true,
			errSubstr: "load configuration",
		},
		{
			name: "all senders enabled execution success",
			setup: func(t *testing.T) {
				mDir, oDir := setupAllSecrets(t)
				setupEnvDirs(t, mDir, oDir)
				newServer = mockServerClosedFactory
			},
			wantErr: false,
		},
		{
			name: "telegram only enabled execution success",
			setup: func(t *testing.T) {
				mDir, oDir := setupTelegramOnlySecrets(t)
				setupEnvDirs(t, mDir, oDir)
				t.Setenv("NOTIFY_SMTP_ENABLED", "false")
				t.Setenv("NOTIFY_ADMIN_FILTER_REQUIRED", "false")
				newServer = mockServerClosedFactory
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)

			err := run()
			if (err != nil) != tt.wantErr {
				t.Fatalf("run() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.errSubstr)) {
				t.Fatalf("expected error containing %q, got %v", tt.errSubstr, err)
			}
		})
	}
}

func TestRunServer(t *testing.T) {
	tests := []struct {
		ctxBuilder func(t *testing.T) (context.Context, context.CancelFunc)
		srvBuilder func(cancel context.CancelFunc) serverRunner
		name       string
		socketPath string
		siteName   string
		errSubstr  string
		wantErr    bool
	}{
		{
			name:       "clean stop with ErrServerClosed",
			socketPath: "/tmp/notify1.sock",
			siteName:   "Alpha Site",
			ctxBuilder: func(_ *testing.T) (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			srvBuilder: func(_ context.CancelFunc) serverRunner {
				return &mockServer{listenErr: http.ErrServerClosed}
			},
			wantErr: false,
		},
		{
			name:       "clean stop with nil error",
			socketPath: "/tmp/notify2.sock",
			siteName:   "Beta Site",
			ctxBuilder: func(_ *testing.T) (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			srvBuilder: func(_ context.CancelFunc) serverRunner {
				return &mockServer{listenErr: nil}
			},
			wantErr: false,
		},
		{
			name:       "listen failure returns fatal error",
			socketPath: "/tmp/notify3.sock",
			siteName:   "Gamma Site",
			ctxBuilder: func(_ *testing.T) (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			srvBuilder: func(_ context.CancelFunc) serverRunner {
				return &mockServer{listenErr: errors.New("socket bind failed")}
			},
			wantErr:   true,
			errSubstr: "server fatal error",
		},
		{
			name:       "signal cancellation triggers graceful shutdown success",
			socketPath: "/tmp/notify4.sock",
			siteName:   "Delta Site",
			ctxBuilder: func(_ *testing.T) (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			srvBuilder: func(cancel context.CancelFunc) serverRunner {
				return &mockServer{
					started:      make(chan struct{}),
					shutdownDone: make(chan struct{}),
					listenHook:   cancel,
					listenErr:    http.ErrServerClosed,
				}
			},
			wantErr: false,
		},
		{
			name:       "graceful shutdown failure returns error",
			socketPath: "/tmp/notify5.sock",
			siteName:   "Epsilon Site",
			ctxBuilder: func(_ *testing.T) (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			srvBuilder: func(cancel context.CancelFunc) serverRunner {
				return &mockServer{
					started:      make(chan struct{}),
					shutdownDone: make(chan struct{}),
					listenHook:   cancel,
					listenErr:    http.ErrServerClosed,
					shutdownErr:  errors.New("timeout reached"),
				}
			},
			wantErr:   true,
			errSubstr: "graceful shutdown failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.ctxBuilder(t)
			defer cancel()

			srv := tt.srvBuilder(cancel)
			err := runServer(ctx, srv, tt.socketPath, tt.siteName)

			if (err != nil) != tt.wantErr {
				t.Fatalf("runServer() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.errSubstr)) {
				t.Fatalf("expected error containing %q, got %v", tt.errSubstr, err)
			}
		})
	}
}

func TestDefaultFactories(t *testing.T) {
	cfg := &config.Config{
		SocketPath: "/tmp/default_factory.sock",
		SiteName:   "Zeta Site",
	}
	r := router.NewRouter(cfg, nil)
	limiter := ratelimit.NewLimiter(10, 5)
	srv := defaultNewServer(cfg, r, limiter)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func mockHealthyCheck(_ context.Context, _ string) error {
	return nil
}

func mockFailingCheck(_ context.Context, _ string) error {
	return errors.New("socket unreachable")
}

func TestIsHealthcheckArg(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"healthcheck", true},
		{"-healthcheck", true},
		{"--healthcheck", true},
		{"status", false},
		{"--help", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			if got := isHealthcheckArg(tt.arg); got != tt.want {
				t.Fatalf("isHealthcheckArg(%q) = %v, want %v", tt.arg, got, tt.want)
			}
		})
	}
}

func TestResolveHealthcheckSocket(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	tests := []struct {
		name      string
		extraArg  string
		envSocket string
		want      string
	}{
		{
			name:      "cli argument priority",
			extraArg:  "/tmp/custom_cli.sock",
			envSocket: "/tmp/custom_env_cli.sock",
			want:      "/tmp/custom_cli.sock",
		},
		{
			name:      "environment variable priority",
			extraArg:  "",
			envSocket: "/tmp/custom_env_var.sock",
			want:      "/tmp/custom_env_var.sock",
		},
		{
			name:      "default fallback",
			extraArg:  "",
			envSocket: "",
			want:      defaultSocketPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.extraArg != "" {
				os.Args = []string{"test-bin", "cmd-arg", tt.extraArg}
			} else {
				os.Args = []string{"test-bin", "cmd-arg"}
			}
			t.Setenv("NOTIFY_SOCKET_PATH", tt.envSocket)

			if got := resolveHealthcheckSocket(); got != tt.want {
				t.Fatalf("resolveHealthcheckSocket() = %q, want %q", got, tt.want)
			}
		})
	}
}

type mockPipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMockPipeListener() *mockPipeListener {
	return &mockPipeListener{
		conns:  make(chan net.Conn, 16),
		closed: make(chan struct{}),
	}
}

func (m *mockPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-m.conns:
		return conn, nil
	case <-m.closed:
		return nil, net.ErrClosed
	}
}

func (m *mockPipeListener) Close() error {
	m.once.Do(func() {
		close(m.closed)
	})
	return nil
}

func (m *mockPipeListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "pipe", Net: "unix"}
}

func (m *mockPipeListener) Dial() (net.Conn, error) {
	select {
	case <-m.closed:
		return nil, net.ErrClosed
	default:
	}
	serverConn, clientConn := net.Pipe()
	m.conns <- serverConn
	return clientConn, nil
}

func (m *mockPipeListener) DialContext(_ context.Context, _ string) (net.Conn, error) {
	return m.Dial()
}

func startMockHealthServer(t *testing.T, statusCode int) (listener *mockPipeListener, cleanup func()) {
	t.Helper()
	listener = newMockPipeListener()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(statusCode)
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() {
		if sErr := server.Serve(listener); sErr != nil && !errors.Is(sErr, http.ErrServerClosed) {
			t.Errorf("serve error: %v", sErr)
		}
	}()
	cleanup = func() {
		if cErr := server.Close(); cErr != nil && !errors.Is(cErr, http.ErrServerClosed) {
			t.Errorf("server close error: %v", cErr)
		}
		if lErr := listener.Close(); lErr != nil && !errors.Is(lErr, net.ErrClosed) {
			t.Errorf("listener close error: %v", lErr)
		}
	}
	return listener, cleanup
}

type mockContextDialer struct {
	conn net.Conn
	err  error
}

func (m *mockContextDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return m.conn, m.err
}

type errCloser struct {
	io.Reader
}

func (e *errCloser) Close() error {
	return errors.New("simulated body close error")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDefaultDialer(t *testing.T) {
	d := defaultDialer()
	if d == nil {
		t.Fatal("expected non-nil default dialer")
	}
}

func TestDefaultNewHTTPClient(t *testing.T) {
	client := defaultNewHTTPClient(defaultSocketPath)
	if client == nil {
		t.Fatal("expected non-nil http client")
	}
}

func TestDefaultDialContext(t *testing.T) {
	origDialer := newDialer
	defer func() { newDialer = origDialer }()

	serverConn, clientConn := net.Pipe()
	defer func() {
		if cErr := serverConn.Close(); cErr != nil {
			t.Errorf("close server conn: %v", cErr)
		}
	}()

	tests := []struct {
		conn      net.Conn
		dialErr   error
		name      string
		errSubstr string
		wantErr   bool
	}{
		{
			conn:    clientConn,
			dialErr: nil,
			name:    "successful dial returns connection",
			wantErr: false,
		},
		{
			conn:      nil,
			dialErr:   errors.New("socket unreachable"),
			name:      "failed dial returns wrapped error",
			errSubstr: "dial unix socket",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockContextDialer{conn: tt.conn, err: tt.dialErr}
			newDialer = func() contextDialer { return mock }

			conn, err := defaultDialContext(context.Background(), defaultSocketPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("defaultDialContext() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.errSubstr)) {
				t.Fatalf("expected error containing %q, got %v", tt.errSubstr, err)
			}
			if conn != nil {
				if cErr := conn.Close(); cErr != nil {
					t.Errorf("close conn: %v", cErr)
				}
			}
		})
	}
}

func TestDefaultHealthcheck(t *testing.T) {
	origDialer := socketDialer
	origHTTPClient := newHTTPClient
	defer func() {
		socketDialer = origDialer
		newHTTPClient = origHTTPClient
	}()

	tests := []struct {
		name        string
		errSubstr   string
		handlerCode int
		closeErr    bool
		wantErr     bool
	}{
		{
			name:        "healthy server returns nil",
			errSubstr:   "",
			handlerCode: http.StatusOK,
			closeErr:    false,
			wantErr:     false,
		},
		{
			name:        "unhealthy server returns error",
			errSubstr:   "healthcheck unhealthy status code",
			handlerCode: http.StatusServiceUnavailable,
			closeErr:    false,
			wantErr:     true,
		},
		{
			name:        "close error returns wrapped error",
			errSubstr:   "close healthcheck response",
			handlerCode: http.StatusOK,
			closeErr:    true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.closeErr {
				newHTTPClient = func(_ string) *http.Client {
					return &http.Client{
						Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
							return &http.Response{
								StatusCode: http.StatusOK,
								Body:       &errCloser{Reader: strings.NewReader(`{"status":"ok"}`)},
							}, nil
						}),
					}
				}
			} else {
				listener, cleanup := startMockHealthServer(t, tt.handlerCode)
				defer cleanup()
				socketDialer = listener.DialContext
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			err := defaultHealthcheck(ctx, defaultSocketPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("defaultHealthcheck() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.errSubstr)) {
				t.Fatalf("expected error containing %q, got %v", tt.errSubstr, err)
			}
		})
	}

	t.Run("nil context returns request creation error", func(t *testing.T) {
		var nilCtx context.Context
		nilContextErr := defaultHealthcheck(nilCtx, defaultSocketPath)
		if nilContextErr == nil || !strings.Contains(nilContextErr.Error(), "create healthcheck request") {
			t.Fatalf("expected create healthcheck request error, got %v", nilContextErr)
		}
	})
}

func TestDefaultHealthcheck_InvalidSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := defaultHealthcheck(ctx, filepath.Join(t.TempDir(), "nonexistent.sock"))
	if err == nil || !strings.Contains(err.Error(), "healthcheck probe failed") {
		t.Fatalf("expected probe failure, got %v", err)
	}
}

func TestMainHealthcheck(t *testing.T) {
	origArgs := os.Args
	origExit := osExit
	origHealth := healthcheck
	defer func() {
		os.Args = origArgs
		osExit = origExit
		healthcheck = origHealth
	}()

	osExit = mockExit

	tests := []struct {
		checkFunc    func(context.Context, string) error
		name         string
		wantExitCode int
	}{
		{
			checkFunc:    mockHealthyCheck,
			name:         "successful probe exits with code 0",
			wantExitCode: 0,
		},
		{
			checkFunc:    mockFailingCheck,
			name:         "failed probe exits with code 1",
			wantExitCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testExited = false
			testExitCode = -1

			os.Args = []string{"test-probe", "healthcheck"}
			healthcheck = tt.checkFunc

			main()

			if !testExited {
				t.Fatal("expected main to exit")
			}
			if testExitCode != tt.wantExitCode {
				t.Fatalf("expected exit code %d, got %d", tt.wantExitCode, testExitCode)
			}
		})
	}
}
