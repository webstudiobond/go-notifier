package sender

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
)

var (
	sharedTestCertPool *x509.CertPool
	currentMockOpts    mockSMTPOptions
)

func generateTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	ipList := make([]net.IP, 0, 16)
	for i := 1; i <= 10; i++ {
		ipList = append(ipList, net.IPv4(127, 0, 0, byte(i)))
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Example Test Org"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ipList,
		DNSNames:              []string{"localhost", "mail-cert.example.com"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	tlsCert, err := tls.X509KeyPair(certPEM, privPEM)
	if err != nil {
		t.Fatalf("failed to parse key pair: %v", err)
	}

	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(certPEM)
	return tlsCert, certPool
}

func mockTestTLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		RootCAs:    sharedTestCertPool,
		MinVersion: tls.VersionTLS12,
	}
}

type bufferedPipe struct {
	cond   *sync.Cond
	buf    []byte
	mu     sync.Mutex
	closed bool
}

func newBufferedPipe() *bufferedPipe {
	p := &bufferedPipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufferedPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *bufferedPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 && p.closed {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *bufferedPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
	return nil
}

type bufferedConn struct {
	reader *bufferedPipe
	writer *bufferedPipe
}

func (c *bufferedConn) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c *bufferedConn) Write(b []byte) (int, error) { return c.writer.Write(b) }
func (c *bufferedConn) Close() error {
	if err := c.reader.Close(); err != nil {
		if wErr := c.writer.Close(); wErr != nil {
			return fmt.Errorf("writer close: %w; reader close: %w", wErr, err)
		}
		return err
	}
	return c.writer.Close()
}
func (c *bufferedConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c *bufferedConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c *bufferedConn) SetDeadline(_ time.Time) error      { return nil }
func (c *bufferedConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *bufferedConn) SetWriteDeadline(_ time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "pipe" }
func (dummyAddr) String() string  { return "pipe" }

func newBufferedConnPair() (client, server net.Conn) {
	pipe1 := newBufferedPipe()
	pipe2 := newBufferedPipe()
	client = &bufferedConn{reader: pipe1, writer: pipe2}
	server = &bufferedConn{reader: pipe2, writer: pipe1}
	return client, server
}

func mockFailingDialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return nil, errors.New("simulated dial failure")
}

func mockPipeDialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := newBufferedConnPair()
	go handleMockSMTPConn(ctx, server, currentMockOpts)
	return client, nil
}

func mockErrCloserPipeDialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := newBufferedConnPair()
	go handleMockSMTPConn(ctx, server, currentMockOpts)
	return &errCloserConn{Conn: client}, nil
}

func mockFailingSMTPNewClient(_ net.Conn, _ string) (*smtp.Client, error) {
	return nil, errors.New("simulated smtp client init failure")
}

type errCloserConn struct {
	net.Conn
}

func (c *errCloserConn) Close() error {
	if err := c.Conn.Close(); err != nil {
		return fmt.Errorf("underlying conn close: %w", err)
	}
	return errors.New("simulated raw connection close failure")
}

type mockDataWriter struct {
	writeErr error
	closeErr error
}

func (m *mockDataWriter) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(p), nil
}

func (m *mockDataWriter) Close() error {
	return m.closeErr
}

func mockFailingWriteCloseWriter(_ *smtp.Client) (io.WriteCloser, error) {
	return &mockDataWriter{
		writeErr: errors.New("simulated data write stream error"),
		closeErr: errors.New("simulated data close stream error"),
	}, nil
}

func mockFailingWriteCleanCloseWriter(_ *smtp.Client) (io.WriteCloser, error) {
	return &mockDataWriter{
		writeErr: errors.New("simulated data write stream error"),
		closeErr: nil,
	}, nil
}

type mockSMTPOptions struct {
	serverTLSConfig *tls.Config
	dataSink        func([]string)
	authErr         bool
	mailErr         bool
	rcptErr         bool
	dataErr         bool
	dataCloseErr    bool
	supportTLS      bool
	startTLSErr     bool
	implicitTLS     bool
}

func writeMockLine(conn net.Conn, s string) error {
	_, err := conn.Write([]byte(s))
	return err
}

func handleMockGreeting(ctx context.Context, conn net.Conn, line string, opts mockSMTPOptions) (net.Conn, bool, error) {
	switch {
	case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
		if opts.supportTLS {
			return conn, false, writeMockLine(conn, "250-mail-mock.example.com\r\n250-AUTH PLAIN\r\n250 STARTTLS\r\n")
		}
		return conn, false, writeMockLine(conn, "250-mail-mock.example.com\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n")
	case strings.HasPrefix(line, "STARTTLS"):
		if opts.startTLSErr {
			return conn, false, writeMockLine(conn, "454 TLS not available\r\n")
		}
		if err := writeMockLine(conn, "220 2.0.0 Ready to start TLS\r\n"); err != nil {
			return conn, true, err
		}
		tlsConn := tls.Server(conn, opts.serverTLSConfig)
		if hErr := tlsConn.HandshakeContext(ctx); hErr != nil {
			return conn, true, hErr
		}
		return tlsConn, false, nil
	default:
		return conn, false, nil
	}
}

func handleMockMailAndAuth(conn net.Conn, line string, opts mockSMTPOptions) error {
	switch {
	case strings.HasPrefix(line, "AUTH PLAIN"):
		if opts.authErr {
			return writeMockLine(conn, "535 5.7.8 Authentication credentials invalid\r\n")
		}
		return writeMockLine(conn, "235 2.7.0 Authentication successful\r\n")
	case strings.HasPrefix(line, "MAIL FROM:"):
		if opts.mailErr {
			return writeMockLine(conn, "550 5.1.8 Sender rejected\r\n")
		}
		return writeMockLine(conn, "250 2.1.0 Ok\r\n")
	case strings.HasPrefix(line, "RCPT TO:"):
		if opts.rcptErr {
			return writeMockLine(conn, "550 5.1.1 Recipient rejected\r\n")
		}
		return writeMockLine(conn, "250 2.1.5 Ok\r\n")
	default:
		return nil
	}
}

func handleMockData(conn net.Conn, reader *textproto.Reader, opts mockSMTPOptions) error {
	if opts.dataErr {
		return writeMockLine(conn, "451 4.3.0 Cannot accept data\r\n")
	}
	if err := writeMockLine(conn, "354 End data with <CR><LF>.<CR><LF>\r\n"); err != nil {
		return err
	}
	var lines []string
	for {
		dataLine, dErr := reader.ReadLine()
		if dErr != nil {
			return dErr
		}
		if dataLine == "QUIT" {
			if wErr := writeMockLine(conn, "221 2.0.0 Bye\r\n"); wErr != nil {
				return wErr
			}
			return errors.New("client quit during data")
		}
		if dataLine == "." {
			break
		}
		if opts.dataSink != nil {
			lines = append(lines, dataLine)
		}
	}
	if opts.dataSink != nil {
		opts.dataSink(lines)
	}
	if opts.dataCloseErr {
		return writeMockLine(conn, "554 5.6.0 Transaction failed\r\n")
	}
	return writeMockLine(conn, "250 2.0.0 Ok: queued\r\n")
}

func handleMockControl(conn net.Conn, line string) (handled, shouldContinue bool) {
	switch line {
	case "QUIT":
		if wErr := writeMockLine(conn, "221 2.0.0 Bye\r\n"); wErr != nil {
			return true, false
		}
		return true, false
	case "*":
		if wErr := writeMockLine(conn, "501 5.7.0 Authentication cancelled\r\n"); wErr != nil {
			return true, false
		}
		return true, true
	default:
		return false, true
	}
}

func processSMTPCommand(ctx context.Context, conn net.Conn, reader *textproto.Reader, line string, opts mockSMTPOptions) (net.Conn, bool) {
	if handled, shouldContinue := handleMockControl(conn, line); handled {
		return conn, shouldContinue
	}

	if strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO") || strings.HasPrefix(line, "STARTTLS") {
		newConn, abort, gErr := handleMockGreeting(ctx, conn, line, opts)
		if abort || gErr != nil {
			return conn, false
		}
		return newConn, true
	}

	if strings.HasPrefix(line, "AUTH PLAIN") || strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:") {
		if aErr := handleMockMailAndAuth(conn, line, opts); aErr != nil {
			return conn, false
		}
		return conn, true
	}

	if line == "DATA" {
		if dErr := handleMockData(conn, reader, opts); dErr != nil {
			return conn, false
		}
	}
	return conn, true
}

func handleMockSMTPConn(ctx context.Context, conn net.Conn, opts mockSMTPOptions) {
	defer func() {
		if cErr := conn.Close(); cErr != nil {
			return
		}
	}()
	if opts.implicitTLS {
		tlsConn := tls.Server(conn, opts.serverTLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return
		}
		conn = tlsConn
	}
	if err := writeMockLine(conn, "220 mail-mock.example.com ESMTP Test Server\r\n"); err != nil {
		return
	}
	reader := textproto.NewReader(bufio.NewReader(conn))

	for {
		line, err := reader.ReadLine()
		if err != nil {
			return
		}

		newConn, shouldContinue := processSMTPCommand(ctx, conn, reader, line, opts)
		if !shouldContinue {
			return
		}
		if newConn != conn {
			conn = newConn
			reader = textproto.NewReader(bufio.NewReader(conn))
		}
	}
}

func TestSMTPSender_Name(t *testing.T) {
	cfg := config.SMTPConfig{}
	sender := NewSMTPSender(&cfg, "Site")
	if sender.Name() != "smtp" {
		t.Fatalf("expected Name 'smtp', got %q", sender.Name())
	}
}

func TestSMTPSender_Send_EmptyRecipients(t *testing.T) {
	cfg := config.SMTPConfig{
		Host: "mail-empty.example.com",
		Port: 465,
	}
	sender := NewSMTPSender(&cfg, "Site")
	msg := &Message{
		To: []string{},
	}
	err := sender.Send(context.Background(), msg, false)
	if err == nil || !strings.Contains(err.Error(), "recipient list is empty") {
		t.Fatalf("expected empty recipient error, got %v", err)
	}
}

func TestSMTPSender_BuildMIMEMessage_Branches(t *testing.T) {
	tests := []struct {
		name       string
		siteName   string
		fromName   string
		replyTo    string
		bodyText   string
		bodyHTML   string
		wantHeader string
		wantBody   string
		attach     []Attachment
	}{
		{
			name:       "fallback_from_name_to_site_name",
			siteName:   "Fallback Site",
			fromName:   "",
			replyTo:    "",
			bodyText:   "Plain Text Alert",
			bodyHTML:   "",
			wantHeader: "From: Fallback Site <sender@example.com>",
			wantBody:   "Content-Type: text/plain",
		},
		{
			name:       "empty_from_name_and_site_name",
			siteName:   "",
			fromName:   "",
			replyTo:    "reply@example.org",
			bodyText:   "",
			bodyHTML:   "<h1>HTML Alert Only</h1>",
			wantHeader: "From: sender@example.com",
			wantBody:   "Content-Type: text/html",
		},
		{
			name:       "both_text_and_html_without_attachments",
			siteName:   "Dual Body Site",
			fromName:   "Dual Admin",
			replyTo:    "",
			bodyText:   "Dual plain text",
			bodyHTML:   "<p>Dual html text</p>",
			wantHeader: "From: Dual Admin <sender@example.com>",
			wantBody:   "multipart/alternative",
		},
		{
			name:       "attachment_with_empty_mimetype_and_long_base64",
			siteName:   "Attach Site",
			fromName:   "Attach Admin",
			replyTo:    "",
			bodyText:   "Text with attachment",
			bodyHTML:   "",
			wantHeader: "From: Attach Admin <sender@example.com>",
			wantBody:   "application/octet-stream",
			attach: []Attachment{
				{
					Filename:      "binary.dat",
					MIMEType:      "",
					ContentBase64: strings.Repeat("A", 160),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.SMTPConfig{
				Host:     "mail-branch.example.com",
				Port:     465,
				Mail:     "sender@example.com",
				FromName: tt.fromName,
			}
			sender := NewSMTPSender(&cfg, tt.siteName)
			msg := &Message{
				To:          []string{"recipient@example.net"},
				Subject:     "MIME Verification",
				BodyText:    tt.bodyText,
				BodyHTML:    tt.bodyHTML,
				ReplyTo:     tt.replyTo,
				Attachments: tt.attach,
			}

			raw := string(sender.buildMIMEMessage(msg))
			if !strings.Contains(raw, tt.wantHeader) {
				t.Errorf("expected header %q in message: %s", tt.wantHeader, raw)
			}
			if !strings.Contains(raw, tt.wantBody) {
				t.Errorf("expected body substring %q in message: %s", tt.wantBody, raw)
			}
			if tt.replyTo != "" && !strings.Contains(raw, "Reply-To: "+tt.replyTo) {
				t.Errorf("expected Reply-To header in message: %s", raw)
			}
		})
	}
}

type mockContextDialer struct {
	dialFunc func(ctx context.Context, network, address string) (net.Conn, error)
}

func (m *mockContextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return m.dialFunc(ctx, network, address)
}

func mockFailingDialer() contextDialer {
	return &mockContextDialer{
		dialFunc: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("simulated dialer failure")
		},
	}
}

func mockSuccessDialer() contextDialer {
	return &mockContextDialer{
		dialFunc: func(_ context.Context, _, _ string) (net.Conn, error) {
			client, _ := newBufferedConnPair()
			return client, nil
		},
	}
}

func TestSMTPSender_DefaultSMTPDialContext(t *testing.T) {
	t.Run("default_dialer_construction", func(t *testing.T) {
		d := defaultDialer()
		if d == nil {
			t.Fatal("expected non-nil default dialer")
		}
	})

	t.Run("smtp_dial_error", func(t *testing.T) {
		origDialer := newDialer
		defer func() { newDialer = origDialer }()
		newDialer = mockFailingDialer

		_, err := defaultSMTPDialContext(context.Background(), "tcp", "mail.example.org:25")
		if err == nil || !strings.Contains(err.Error(), "dial: ") {
			t.Fatalf("expected dial error, got %v", err)
		}
	})

	t.Run("smtp_dial_success", func(t *testing.T) {
		origDialer := newDialer
		defer func() { newDialer = origDialer }()
		newDialer = mockSuccessDialer

		conn, err := defaultSMTPDialContext(context.Background(), "tcp", "mail.example.org:25")
		if err != nil {
			t.Fatalf("unexpected dial error: %v", err)
		}
		if cErr := conn.Close(); cErr != nil {
			t.Fatalf("unexpected close error: %v", cErr)
		}
	})
}

func TestSMTPSender_SendImplicitTLS_Success(t *testing.T) {
	tlsCert, certPool := generateTestCertificate(t)
	sharedTestCertPool = certPool

	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}

	currentMockOpts = mockSMTPOptions{
		serverTLSConfig: serverTLSConfig,
		implicitTLS:     true,
	}

	origTLSConfig := newTLSConfig
	defer func() { newTLSConfig = origTLSConfig }()
	newTLSConfig = mockTestTLSConfig

	origDial := smtpDialContext
	defer func() { smtpDialContext = origDial }()
	smtpDialContext = mockPipeDialContext

	cfg := config.SMTPConfig{
		Host:     "127.0.0.1",
		Port:     465,
		Mail:     "sender-tls@example.com",
		Password: "testpassword-tls",
		FromName: "Implicit TLS Sender",
	}

	sender := NewSMTPSender(&cfg, "TLS Site")

	msg := &Message{
		To:       []string{"client@example.org"},
		Subject:  "Implicit TLS Verification",
		BodyText: "Secure message over implicit TLS",
	}

	err := sender.Send(context.Background(), msg, false)
	if err != nil {
		t.Fatalf("unexpected Send error over implicit TLS: %v", err)
	}
}

func TestSMTPSender_SendImplicitTLS_DialAndHandshakeErrors(t *testing.T) {
	tests := []struct {
		dialHook func(context.Context, string, string) (net.Conn, error)
		name     string
		wantErr  string
	}{
		{
			dialHook: mockFailingDialContext,
			name:     "dial_error_returns_wrapped_error",
			wantErr:  "dial smtp tls",
		},
		{
			dialHook: mockPipeDialContext,
			name:     "handshake_error_on_plain_server",
			wantErr:  "smtp tls handshake",
		},
		{
			dialHook: mockErrCloserPipeDialContext,
			name:     "handshake_error_with_failing_raw_conn_close",
			wantErr:  "close raw connection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentMockOpts = mockSMTPOptions{implicitTLS: false}

			origDial := smtpDialContext
			defer func() { smtpDialContext = origDial }()
			smtpDialContext = tt.dialHook

			cfg := config.SMTPConfig{
				Host: "mail-implicit-err.example.org",
				Port: 465,
			}
			sender := NewSMTPSender(&cfg, "Dial Error Site")
			msg := &Message{
				To:       []string{"rcpt-implicit-err@example.com"},
				Subject:  "Implicit Error Test",
				BodyText: "Body Implicit Error",
			}
			err := sender.Send(context.Background(), msg, false)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSMTPSender_SendImplicitTLS_ClientInitError(t *testing.T) {
	tlsCert, certPool := generateTestCertificate(t)
	sharedTestCertPool = certPool

	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}

	currentMockOpts = mockSMTPOptions{
		serverTLSConfig: serverTLSConfig,
		implicitTLS:     true,
	}

	origTLSConfig := newTLSConfig
	defer func() { newTLSConfig = origTLSConfig }()
	newTLSConfig = mockTestTLSConfig

	origClient := smtpNewClient
	defer func() { smtpNewClient = origClient }()
	smtpNewClient = mockFailingSMTPNewClient

	tests := []struct {
		dialHook func(context.Context, string, string) (net.Conn, error)
		name     string
		wantErr  string
	}{
		{
			dialHook: mockPipeDialContext,
			name:     "clean_close",
			wantErr:  "init smtp client",
		},
		{
			dialHook: mockErrCloserPipeDialContext,
			name:     "failing_close",
			wantErr:  "close tls connection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origDial := smtpDialContext
			defer func() { smtpDialContext = origDial }()
			smtpDialContext = tt.dialHook

			cfg := config.SMTPConfig{
				Host: "mail-cert.example.com",
				Port: 465,
			}
			sender := NewSMTPSender(&cfg, "Init Error Site")

			msg := &Message{
				To:       []string{"rcpt-init@example.org"},
				Subject:  "Client Init Error",
				BodyText: "Body Init Error",
			}
			err := sender.Send(context.Background(), msg, false)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSMTPSender_SendStartTLS_SuccessModes(t *testing.T) {
	tlsCert, certPool := generateTestCertificate(t)
	sharedTestCertPool = certPool

	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}

	tests := []struct {
		name       string
		mail       string
		recipient  string
		subject    string
		body       string
		port       int
		supportTLS bool
	}{
		{
			name:       "starttls_negotiated",
			mail:       "starttls@example.com",
			recipient:  "rcpt-starttls-ok@example.net",
			subject:    "StartTLS Success",
			body:       "Secure message over StartTLS",
			port:       587,
			supportTLS: true,
		},
		{
			name:       "plain_without_starttls_extension",
			mail:       "plain@example.com",
			recipient:  "rcpt-plain-ok@example.org",
			subject:    "Plain SMTP Success",
			body:       "Delivery without StartTLS extension",
			port:       25,
			supportTLS: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentMockOpts = mockSMTPOptions{
				supportTLS: tt.supportTLS,
			}
			if tt.supportTLS {
				currentMockOpts.serverTLSConfig = serverTLSConfig
			}

			origTLSConfig := newTLSConfig
			defer func() { newTLSConfig = origTLSConfig }()
			newTLSConfig = mockTestTLSConfig

			cfg := config.SMTPConfig{
				Host:     "localhost",
				Port:     tt.port,
				Mail:     tt.mail,
				Password: "test",
			}
			sender := NewSMTPSender(&cfg, "StartTLS Site")

			origDial := smtpDialContext
			defer func() { smtpDialContext = origDial }()
			smtpDialContext = mockPipeDialContext

			msg := &Message{
				To:       []string{tt.recipient},
				Subject:  tt.subject,
				BodyText: tt.body,
			}
			err := sender.Send(context.Background(), msg, false)
			if err != nil {
				t.Fatalf("unexpected Send error in %s: %v", tt.name, err)
			}
		})
	}
}

func TestSMTPSender_SendStartTLS_Errors(t *testing.T) {
	tests := []struct {
		dialHook   func(context.Context, string, string) (net.Conn, error)
		clientHook func(net.Conn, string) (*smtp.Client, error)
		name       string
		wantErr    string
		opts       mockSMTPOptions
	}{
		{
			dialHook: mockFailingDialContext,
			name:     "dial_error",
			wantErr:  "dial smtp ",
		},
		{
			dialHook:   mockPipeDialContext,
			clientHook: mockFailingSMTPNewClient,
			name:       "init_client_error",
			wantErr:    "init smtp client",
		},
		{
			dialHook:   mockErrCloserPipeDialContext,
			clientHook: mockFailingSMTPNewClient,
			name:       "init_client_close_error",
			wantErr:    "close connection",
		},
		{
			dialHook: mockPipeDialContext,
			name:     "starttls_command_rejected_by_server",
			wantErr:  "smtp starttls",
			opts:     mockSMTPOptions{supportTLS: true, startTLSErr: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentMockOpts = tt.opts

			origDial := smtpDialContext
			defer func() { smtpDialContext = origDial }()
			smtpDialContext = tt.dialHook

			if tt.clientHook != nil {
				origClient := smtpNewClient
				defer func() { smtpNewClient = origClient }()
				smtpNewClient = tt.clientHook
			}

			cfg := config.SMTPConfig{
				Host: "mail-starttls-err.example.org",
				Port: 587,
			}
			sender := NewSMTPSender(&cfg, "StartTLS Error Site")
			msg := &Message{
				To:       []string{"rcpt-st-table@example.net"},
				Subject:  "StartTLS Error",
				BodyText: "Body StartTLS Error Test",
			}
			err := sender.Send(context.Background(), msg, false)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSMTPSender_ExecuteSMTPTransaction_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := config.SMTPConfig{
		Host: "mail-cancel.example.com",
	}
	sender := NewSMTPSender(&cfg, "Cancel Site")

	err := sender.executeSMTPTransaction(ctx, nil, []byte("data"), []string{"rcpt-cancel@example.com"})
	if err == nil || !strings.Contains(err.Error(), "smtp transaction cancelled") {
		t.Fatalf("expected transaction cancelled error, got %v", err)
	}
}

func TestSMTPSender_ExecuteSMTPTransaction_Errors(t *testing.T) {
	tests := []struct {
		dialHook   func(context.Context, string, string) (net.Conn, error)
		clientHook func(net.Conn, string) (*smtp.Client, error)
		dataHook   func(*smtp.Client) (io.WriteCloser, error)
		name       string
		body       string
		wantErr    string
		opts       mockSMTPOptions
	}{
		{
			dialHook: mockPipeDialContext,
			name:     "auth_failure",
			wantErr:  "smtp authentication failed",
			opts:     mockSMTPOptions{authErr: true},
		},
		{
			dialHook: mockPipeDialContext,
			name:     "mail_from_failure",
			wantErr:  "smtp mail from failed",
			opts:     mockSMTPOptions{mailErr: true},
		},
		{
			dialHook: mockPipeDialContext,
			name:     "rcpt_to_failure",
			wantErr:  "smtp rcpt to",
			opts:     mockSMTPOptions{rcptErr: true},
		},
		{
			dialHook: mockPipeDialContext,
			name:     "data_command_failure",
			wantErr:  "smtp data command failed",
			opts:     mockSMTPOptions{dataErr: true},
		},
		{
			dialHook: mockPipeDialContext,
			name:     "data_close_failure",
			wantErr:  "smtp close data writer failed",
			opts:     mockSMTPOptions{dataCloseErr: true},
		},
		{
			dialHook: mockPipeDialContext,
			dataHook: mockFailingWriteCloseWriter,
			name:     "write_failure_close_failure",
			wantErr:  "close data writer",
		},
		{
			dialHook: mockPipeDialContext,
			dataHook: mockFailingWriteCleanCloseWriter,
			name:     "write_failure_close_success",
			wantErr:  "smtp write message data failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentMockOpts = tt.opts

			cfg := config.SMTPConfig{
				Host:     "127.0.0.1",
				Port:     25,
				Mail:     "sender-proto@example.com",
				Password: "test",
			}
			sender := NewSMTPSender(&cfg, "Protocol Site")

			origDial := smtpDialContext
			defer func() { smtpDialContext = origDial }()
			smtpDialContext = tt.dialHook

			if tt.clientHook != nil {
				origNewClient := smtpNewClient
				defer func() { smtpNewClient = origNewClient }()
				smtpNewClient = tt.clientHook
			}

			if tt.dataHook != nil {
				origData := smtpClientData
				defer func() { smtpClientData = origData }()
				smtpClientData = tt.dataHook
			}

			body := tt.body
			if body == "" {
				body = "Test Content"
			}

			msg := &Message{
				To:       []string{"rcpt-proto@example.com"},
				Subject:  "Protocol Failure Test",
				BodyText: body,
			}

			err := sender.Send(context.Background(), msg, false)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		fallback string
		want     string
	}{
		{
			name:     "standard_email",
			addr:     "user@example.com",
			fallback: "fallback.example.org",
			want:     "example.com",
		},
		{
			name:     "name_and_address",
			addr:     "\"Test User\" <user@example.org>",
			fallback: "fallback.example.net",
			want:     "example.org",
		},
		{
			name:     "subdomain_address",
			addr:     "notify@sub.example.net",
			fallback: "fallback.example.com",
			want:     "sub.example.net",
		},
		{
			name:     "uppercase_domain",
			addr:     "admin@UPPER.EXAMPLE.ORG",
			fallback: "fallback.example.com",
			want:     "upper.example.org",
		},
		{
			name:     "invalid_address_host_port",
			addr:     "invalid-addr-1",
			fallback: "mail.example.org:587",
			want:     "mail.example.org",
		},
		{
			name:     "invalid_address_host_only",
			addr:     "invalid-addr-2",
			fallback: "mail.example.com",
			want:     "mail.example.com",
		},
		{
			name:     "empty_address_with_fallback",
			addr:     "",
			fallback: "fallback-empty.example.org",
			want:     "fallback-empty.example.org",
		},
		{
			name:     "empty_address_and_fallback",
			addr:     "",
			fallback: "",
			want:     "example.invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDomain(tt.addr, tt.fallback)
			if got != tt.want {
				t.Errorf("extractDomain(%q, %q) = %q, want %q", tt.addr, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestExtractHostDomain(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		fallback string
		want     string
	}{
		{
			name:     "host_with_port",
			host:     "mail.example.org:587",
			fallback: "admin@example.net",
			want:     "mail.example.org",
		},
		{
			name:     "host_without_port",
			host:     "smtp.example.net",
			fallback: "admin@example.org",
			want:     "smtp.example.net",
		},
		{
			name:     "empty_host_fallback_to_mail",
			host:     "",
			fallback: "ops@alpha.example.com",
			want:     "alpha.example.com",
		},
		{
			name:     "empty_host_and_fallback",
			host:     "",
			fallback: "",
			want:     "example.invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHostDomain(tt.host, tt.fallback)
			if got != tt.want {
				t.Errorf("extractHostDomain(%q, %q) = %q, want %q", tt.host, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestSMTPSender_GenerateMessageID(t *testing.T) {
	cfg := config.SMTPConfig{
		Mail: "service@example.org",
		Host: "smtp.example.org",
	}
	s := NewSMTPSender(&cfg, "Example Site")

	mid1 := s.generateMessageID()
	mid2 := s.generateMessageID()

	if mid1 == mid2 {
		t.Fatalf("expected unique message IDs, got duplicate %q", mid1)
	}

	re := regexp.MustCompile(`^<\d+\.[0-9a-f]{24}@smtp\.example\.org>$`)
	if !re.MatchString(mid1) {
		t.Errorf("mid1 %q does not match expected RFC 5322 format", mid1)
	}
	if !re.MatchString(mid2) {
		t.Errorf("mid2 %q does not match expected RFC 5322 format", mid2)
	}
}

func TestSMTPSender_BuildMIMEMessage_MessageID(t *testing.T) {
	cfg := config.SMTPConfig{
		Mail: "dispatcher@example.net",
		Host: "mail.example.net",
	}
	sender := NewSMTPSender(&cfg, "Notification Hub")

	msg1 := &Message{
		To:       []string{"recipient1@example.com"},
		Subject:  "First Subject",
		BodyText: "First Body",
	}
	msg2 := &Message{
		To:       []string{"recipient2@example.com"},
		Subject:  "Second Subject",
		BodyText: "Second Body",
	}

	raw1 := string(sender.buildMIMEMessage(msg1))
	raw2 := string(sender.buildMIMEMessage(msg2))

	extractMID := func(raw string) string {
		for line := range strings.SplitSeq(raw, "\r\n") {
			if rest, ok := strings.CutPrefix(line, "Message-ID: "); ok {
				return rest
			}
		}
		return ""
	}

	mid1 := extractMID(raw1)
	mid2 := extractMID(raw2)

	if mid1 == "" {
		t.Fatal("expected Message-ID header in first message")
	}
	if mid2 == "" {
		t.Fatal("expected Message-ID header in second message")
	}
	if mid1 == mid2 {
		t.Fatalf("expected different Message-ID headers, got %q", mid1)
	}

	if !strings.HasPrefix(mid1, "<") || !strings.HasSuffix(mid1, "@mail.example.net>") {
		t.Errorf("expected Message-ID format <...@mail.example.net>, got %q", mid1)
	}
	if !strings.HasPrefix(mid2, "<") || !strings.HasSuffix(mid2, "@mail.example.net>") {
		t.Errorf("expected Message-ID format <...@mail.example.net>, got %q", mid2)
	}
}

func TestSMTPSender_Send_MessageIDHeaderOverWire(t *testing.T) {
	tlsCert, certPool := generateTestCertificate(t)
	sharedTestCertPool = certPool

	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}

	var capturedLines [][]string
	currentMockOpts = mockSMTPOptions{
		serverTLSConfig: serverTLSConfig,
		implicitTLS:     true,
		dataSink: func(lines []string) {
			capturedLines = append(capturedLines, lines)
		},
	}

	origTLSConfig := newTLSConfig
	defer func() { newTLSConfig = origTLSConfig }()
	newTLSConfig = mockTestTLSConfig

	origDial := smtpDialContext
	defer func() { smtpDialContext = origDial }()
	smtpDialContext = mockPipeDialContext

	cfg := config.SMTPConfig{
		Host:     "127.0.0.3",
		Port:     465,
		Mail:     "wire-sender@example.com",
		Password: "wire-password",
		FromName: "Wire Sender",
	}
	sender := NewSMTPSender(&cfg, "Wire Site")

	msg1 := &Message{
		To:       []string{"wire-rcpt1@example.org"},
		Subject:  "Wire Test 1",
		BodyText: "Wire Body 1",
	}
	if err := sender.Send(context.Background(), msg1, false); err != nil {
		t.Fatalf("first Send failed: %v", err)
	}

	msg2 := &Message{
		To:       []string{"wire-rcpt2@example.org"},
		Subject:  "Wire Test 2",
		BodyText: "Wire Body 2",
	}
	if err := sender.Send(context.Background(), msg2, false); err != nil {
		t.Fatalf("second Send failed: %v", err)
	}

	if len(capturedLines) != 2 {
		t.Fatalf("expected 2 captured messages, got %d", len(capturedLines))
	}

	findMID := func(lines []string) string {
		for _, line := range lines {
			if rest, ok := strings.CutPrefix(line, "Message-ID: "); ok {
				return rest
			}
		}
		return ""
	}

	mid1 := findMID(capturedLines[0])
	mid2 := findMID(capturedLines[1])

	if mid1 == "" {
		t.Fatal("expected Message-ID header in first delivered message")
	}
	if mid2 == "" {
		t.Fatal("expected Message-ID header in second delivered message")
	}
	if mid1 == mid2 {
		t.Fatalf("expected distinct Message-IDs, got duplicate %q", mid1)
	}
	if !strings.HasSuffix(mid1, "@127.0.0.3>") || !strings.HasSuffix(mid2, "@127.0.0.3>") {
		t.Errorf("expected Message-IDs to end with @127.0.0.3>, got %q and %q", mid1, mid2)
	}
}

func TestSMTPSender_BuildMIMEMessage_HTMLStructure(t *testing.T) {
	cfg := config.SMTPConfig{
		Host: "smtp-struct.example.com",
		Mail: "sender-struct@example.com",
	}
	sender := NewSMTPSender(&cfg, "MIME Structure Site")

	tests := []struct {
		name         string
		subject      string
		bodyText     string
		bodyHTML     string
		attachments  []Attachment
		wantContains []string
		assertOrder  bool
	}{
		{
			name:     "html_only_generates_single_part_html",
			subject:  "HTML Only Alert",
			bodyText: "",
			bodyHTML: "<p>Important <strong>security</strong> update.</p>",
			wantContains: []string{
				"Content-Type: text/html; charset=UTF-8",
				"Content-Transfer-Encoding: quoted-printable",
				"<p>Important <strong>security</strong> update.</p>",
			},
			assertOrder: false,
		},
		{
			name:     "dual_body_generates_alternative_with_plain_first",
			subject:  "Dual Body Alert",
			bodyText: "Caller verbatim plain text",
			bodyHTML: "<p>Original HTML representation</p>",
			wantContains: []string{
				"Content-Type: multipart/alternative",
				"Content-Type: text/plain",
				"Content-Type: text/html",
				"Caller verbatim plain text",
				"Original HTML representation",
				"Content-Transfer-Encoding: quoted-printable",
			},
			assertOrder: true,
		},
		{
			name:     "html_only_with_attachments_generates_mixed_containing_html",
			subject:  "HTML With Attachment",
			bodyText: "",
			bodyHTML: "<p>Report summary.</p>",
			attachments: []Attachment{
				{
					Filename:      "report.pdf",
					MIMEType:      "application/pdf",
					ContentBase64: "cGRmZGF0YQ==",
				},
			},
			wantContains: []string{
				"Content-Type: multipart/mixed; boundary=",
				"Content-Type: text/html; charset=UTF-8",
				"filename=\"report.pdf\"",
			},
		},
		{
			name:     "dual_body_with_attachments_generates_mixed_containing_alternative",
			subject:  "Dual Body With Attachment",
			bodyText: "Caller plain text summary",
			bodyHTML: "<p>Report summary.</p>",
			attachments: []Attachment{
				{
					Filename:      "audit.csv",
					MIMEType:      "text/csv",
					ContentBase64: "Y3N2",
				},
			},
			wantContains: []string{
				"Content-Type: multipart/mixed",
				"Content-Type: multipart/alternative",
				"filename=\"audit.csv\"",
			},
		},
		{
			name:     "text_only_with_attachments_generates_mixed_without_alternative",
			subject:  "Text With Attachment",
			bodyText: "Only plain text",
			attachments: []Attachment{
				{
					Filename:      "image.png",
					MIMEType:      "image/png",
					ContentBase64: "dGV4dA==",
				},
			},
			wantContains: []string{
				"Content-Type: multipart/mixed",
				"Only plain text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{
				To:          []string{"rcpt-struct@example.org"},
				Subject:     tt.subject,
				BodyText:    tt.bodyText,
				BodyHTML:    tt.bodyHTML,
				Attachments: tt.attachments,
			}
			raw := string(sender.buildMIMEMessage(msg))

			for _, want := range tt.wantContains {
				if !strings.Contains(raw, want) {
					t.Errorf("expected %q in raw message, got: %s", want, raw)
				}
			}

			if tt.assertOrder {
				plainIdx := strings.Index(raw, "Content-Type: text/plain")
				htmlIdx := strings.Index(raw, "Content-Type: text/html")
				if plainIdx == -1 || htmlIdx == -1 || plainIdx >= htmlIdx {
					t.Errorf("expected text/plain before text/html according to RFC 2046, got plain=%d html=%d", plainIdx, htmlIdx)
				}
			}
		})
	}
}

type mockFailingWriteCloser struct {
	writeErr error
	closeErr error
}

func (m *mockFailingWriteCloser) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(p), nil
}

func (m *mockFailingWriteCloser) Close() error {
	return m.closeErr
}

func mockQuotedPrintableWriteError(_ io.Writer) io.WriteCloser {
	return &mockFailingWriteCloser{writeErr: errors.New("write failure")}
}

func mockQuotedPrintableCloseError(_ io.Writer) io.WriteCloser {
	return &mockFailingWriteCloser{closeErr: errors.New("close failure")}
}

func TestEncodeQuotedPrintable_Errors(t *testing.T) {
	tests := []struct {
		hook     func(io.Writer) io.WriteCloser
		name     string
		input    string
		expected string
	}{
		{
			name:     "write_error_returns_raw_string",
			hook:     mockQuotedPrintableWriteError,
			input:    "raw string for write error",
			expected: "raw string for write error",
		},
		{
			name:     "close_error_returns_raw_string",
			hook:     mockQuotedPrintableCloseError,
			input:    "raw string for close error",
			expected: "raw string for close error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := newQuotedPrintableWriter
			defer func() { newQuotedPrintableWriter = orig }()
			newQuotedPrintableWriter = tt.hook

			got := encodeQuotedPrintable(tt.input)
			if got != tt.expected {
				t.Errorf("encodeQuotedPrintable() = %q, want %q", got, tt.expected)
			}
		})
	}
}
