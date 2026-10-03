// Package main provides the entrypoint for the wp-notify daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/ratelimit"
	"github.com/webstudiobond/go-notifier/internal/router"
	"github.com/webstudiobond/go-notifier/internal/sender"
	"github.com/webstudiobond/go-notifier/internal/server"
)

const (
	defaultMandatoryStoreDir = "/run/secrets"
	defaultOptionalStoreDir  = "/etc/notifier/secrets"
	defaultSocketPath        = "/var/run/sockets/notify.sock"
	healthcheckTimeout       = 3 * time.Second
	shutdownTimeout          = 5 * time.Second
)

// Version indicates the binary build version injected via linker flags.
var Version = "dev"

type serverRunner interface {
	ListenAndServe(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func defaultNewServer(cfg *config.Config, r *router.Router, limiter *ratelimit.Limiter) serverRunner {
	return server.NewServer(cfg, r, limiter)
}

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func defaultDialer() contextDialer {
	return &net.Dialer{}
}

func defaultDialContext(ctx context.Context, socketPath string) (net.Conn, error) {
	dialer := newDialer()
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial unix socket %s: %w", socketPath, err)
	}
	return conn, nil
}

func defaultNewHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return socketDialer(ctx, socketPath)
			},
		},
		Timeout: healthcheckTimeout,
	}
}

func defaultHealthcheck(ctx context.Context, socketPath string) error {
	client := newHTTPClient(socketPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/healthz", http.NoBody)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck probe failed: %w", err)
	}

	closeErr := resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck unhealthy status code: %d", resp.StatusCode)
	}

	if closeErr != nil {
		return fmt.Errorf("close healthcheck response: %w", closeErr)
	}

	return nil
}

func isHealthcheckArg(arg string) bool {
	return arg == "healthcheck" || arg == "-healthcheck" || arg == "--healthcheck"
}

func resolveHealthcheckSocket() string {
	if len(os.Args) > 2 && os.Args[2] != "" {
		return os.Args[2]
	}
	if envPath := os.Getenv("NOTIFY_SOCKET_PATH"); envPath != "" {
		return envPath
	}
	return defaultSocketPath
}

var (
	newServer     = defaultNewServer
	newDialer     = defaultDialer
	socketDialer  = defaultDialContext
	newHTTPClient = defaultNewHTTPClient
	healthcheck   = defaultHealthcheck
	osExit        = os.Exit
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if len(os.Args) > 1 && isHealthcheckArg(os.Args[1]) {
		ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
		defer cancel()

		if err := healthcheck(ctx, resolveHealthcheckSocket()); err != nil {
			slog.Error("healthcheck failed", "error", err)
			osExit(1)
			return
		}
		osExit(0)
		return
	}

	if err := run(); err != nil {
		slog.Error("daemon terminated with error", "error", err)
		osExit(1)
	}
}

func run() error {
	slog.Info("starting wp-notify daemon", "version", Version)
	mandatoryDir := os.Getenv("MANDATORY_SECRETS_DIR")
	if mandatoryDir == "" {
		mandatoryDir = defaultMandatoryStoreDir
	}

	optionalDir := os.Getenv("OPTIONAL_SECRETS_DIR")
	if optionalDir == "" {
		optionalDir = defaultOptionalStoreDir
	}

	cfg, err := config.LoadConfig(mandatoryDir, optionalDir)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	senders := make(map[string]sender.Sender)
	if cfg.SMTPEnabled {
		senders["smtp"] = sender.NewSMTPSender(&cfg.SMTP, cfg.SiteName)
		slog.Info("smtp sender enabled")
	}

	if cfg.Telegram.Enabled {
		senders["telegram"] = sender.NewTelegramSender(cfg.Telegram.Token, cfg.Telegram.ChatID, cfg.SiteName)
		slog.Info("telegram sender enabled")
	}

	if cfg.Matrix.Enabled {
		senders["matrix"] = sender.NewMatrixSender(cfg.Matrix.URL, cfg.Matrix.RoomID, cfg.Matrix.AccessToken, cfg.SiteName)
		slog.Info("matrix sender enabled")
	}

	if cfg.Ntfy.Enabled {
		senders["ntfy"] = sender.NewNtfySender(cfg.Ntfy.URL, cfg.Ntfy.Topic, cfg.Ntfy.Token, cfg.SiteName)
		slog.Info("ntfy sender enabled")
	}

	limiter := ratelimit.NewLimiter(cfg.RateLimitPerMinute, cfg.BurstLimit)
	r := router.NewRouter(cfg, senders)
	srv := newServer(cfg, r, limiter)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runServer(ctx, srv, cfg.SocketPath, cfg.SiteName)
}

func runServer(ctx context.Context, srv serverRunner, socketPath, siteName string) error {
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting wp-notify daemon", "socket", socketPath, "site", siteName)
		serverErr <- srv.ListenAndServe(ctx)
	}()

	select {
	case sErr := <-serverErr:
		if sErr != nil && !errors.Is(sErr, http.ErrServerClosed) {
			return fmt.Errorf("server fatal error: %w", sErr)
		}
		return nil
	case <-ctx.Done():
		slog.Info("shutting down wp-notify daemon")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		if sErr := srv.Shutdown(shutdownCtx); sErr != nil {
			return fmt.Errorf("graceful shutdown failed: %w", sErr)
		}
		slog.Info("wp-notify daemon stopped successfully")
		return nil
	}
}
