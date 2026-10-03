package sender

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
)

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func defaultDialer() contextDialer {
	return &net.Dialer{Timeout: 30 * time.Second}
}

func defaultSMTPDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := newDialer()
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	return conn, nil
}

func defaultTLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}
}

func defaultSMTPClientData(client *smtp.Client) (io.WriteCloser, error) {
	w, err := client.Data()
	if err != nil {
		return nil, fmt.Errorf("client data: %w", err)
	}
	return w, nil
}

var (
	newDialer       = defaultDialer
	smtpDialContext = defaultSMTPDialContext
	newTLSConfig    = defaultTLSConfig
	smtpNewClient   = smtp.NewClient
	smtpClientData  = defaultSMTPClientData
)

// SMTPSender implements authenticated SMTP relay delivery over TLS.
type SMTPSender struct {
	siteName string
	cfg      config.SMTPConfig
}

// NewSMTPSender creates an initialized SMTPSender driver.
func NewSMTPSender(cfg *config.SMTPConfig, siteName string) *SMTPSender {
	return &SMTPSender{
		siteName: siteName,
		cfg:      *cfg,
	}
}

// Name returns the driver identifier.
func (s *SMTPSender) Name() string {
	return "smtp"
}

// Send builds standard MIME messages with optional attachments and delivers them to the SMTP server.
func (s *SMTPSender) Send(ctx context.Context, msg *Message, _ bool) error {
	if len(msg.To) == 0 {
		return errors.New("cannot deliver mail: recipient list is empty")
	}

	rawMail := s.buildMIMEMessage(msg)
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	if s.cfg.Port == 465 {
		return s.sendImplicitTLS(ctx, addr, rawMail, msg.To)
	}
	return s.sendStartTLS(ctx, addr, rawMail, msg.To)
}

func (s *SMTPSender) sendImplicitTLS(ctx context.Context, addr string, data []byte, recipients []string) error {
	tlsConfig := newTLSConfig(s.cfg.Host)

	rawConn, err := smtpDialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp tls %s: %w", addr, err)
	}

	tlsConn := tls.Client(rawConn, tlsConfig)
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		if closeErr := rawConn.Close(); closeErr != nil {
			return fmt.Errorf("%w: close raw connection: %w", err, closeErr)
		}
		return fmt.Errorf("smtp tls handshake: %w", err)
	}

	client, err := smtpNewClient(tlsConn, s.cfg.Host)
	if err != nil {
		if closeErr := tlsConn.Close(); closeErr != nil {
			return fmt.Errorf("%w: close tls connection: %w", err, closeErr)
		}
		return fmt.Errorf("init smtp client: %w", err)
	}
	defer func() {
		if qErr := client.Quit(); qErr != nil {
			if closeErr := client.Close(); closeErr != nil {
				return
			}
		}
	}()

	return s.executeSMTPTransaction(ctx, client, data, recipients)
}

func (s *SMTPSender) sendStartTLS(ctx context.Context, addr string, data []byte, recipients []string) error {
	conn, err := smtpDialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}

	client, err := smtpNewClient(conn, s.cfg.Host)
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return fmt.Errorf("%w: close connection: %w", err, closeErr)
		}
		return fmt.Errorf("init smtp client: %w", err)
	}
	defer func() {
		if qErr := client.Quit(); qErr != nil {
			if closeErr := client.Close(); closeErr != nil {
				return
			}
		}
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := newTLSConfig(s.cfg.Host)
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	return s.executeSMTPTransaction(ctx, client, data, recipients)
}

func (s *SMTPSender) executeSMTPTransaction(ctx context.Context, client *smtp.Client, data []byte, recipients []string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("smtp transaction cancelled: %w", ctx.Err())
	default:
	}

	auth := smtp.PlainAuth("", s.cfg.Mail, s.cfg.Password, s.cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp authentication failed: %w", err)
	}

	if err := client.Mail(s.cfg.Mail); err != nil {
		return fmt.Errorf("smtp mail from failed: %w", err)
	}

	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to %s failed: %w", rcpt, err)
		}
	}

	w, err := smtpClientData(client)
	if err != nil {
		return fmt.Errorf("smtp data command failed: %w", err)
	}

	if _, err = w.Write(data); err != nil {
		if closeErr := w.Close(); closeErr != nil {
			return fmt.Errorf("%w: close data writer: %w", err, closeErr)
		}
		return fmt.Errorf("smtp write message data failed: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("smtp close data writer failed: %w", err)
	}

	return nil
}

func (s *SMTPSender) generateBoundary() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "=_boundary_" + hex.EncodeToString(b)
}

func encodeMIMEHeader(val string) string {
	for i := range len(val) {
		if val[i] > 127 {
			return mime.BEncoding.Encode("UTF-8", val)
		}
	}
	return val
}

func (s *SMTPSender) buildMIMEMessage(msg *Message) []byte {
	var sb strings.Builder

	fromName := s.cfg.FromName
	if fromName == "" {
		fromName = s.siteName
	}

	fromHeader := s.cfg.Mail
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", encodeMIMEHeader(fromName), s.cfg.Mail)
	}

	sb.WriteString("From: ")
	sb.WriteString(fromHeader)
	sb.WriteString("\r\nTo: ")
	sb.WriteString(strings.Join(msg.To, ", "))
	sb.WriteString("\r\nSubject: ")
	sb.WriteString(encodeMIMEHeader(msg.Subject))
	sb.WriteString("\r\nDate: ")
	sb.WriteString(time.Now().Format(time.RFC1123Z))
	sb.WriteString("\r\nMIME-Version: 1.0\r\n")

	if msg.ReplyTo != "" {
		sb.WriteString("Reply-To: ")
		sb.WriteString(msg.ReplyTo)
		sb.WriteString("\r\n")
	}

	if len(msg.Attachments) > 0 {
		mixedBoundary := s.generateBoundary()
		altBoundary := s.generateBoundary()

		sb.WriteString("Content-Type: multipart/mixed; boundary=\"")
		sb.WriteString(mixedBoundary)
		sb.WriteString("\"\r\n\r\n--")
		sb.WriteString(mixedBoundary)
		sb.WriteString("\r\nContent-Type: multipart/alternative; boundary=\"")
		sb.WriteString(altBoundary)
		sb.WriteString("\"\r\n\r\n")

		s.writeAlternativeParts(&sb, altBoundary, msg)

		sb.WriteString("--")
		sb.WriteString(altBoundary)
		sb.WriteString("--\r\n\r\n")

		for _, att := range msg.Attachments {
			sb.WriteString("--")
			sb.WriteString(mixedBoundary)
			sb.WriteString("\r\n")
			s.writeAttachmentPart(&sb, att)
		}

		sb.WriteString("--")
		sb.WriteString(mixedBoundary)
		sb.WriteString("--\r\n")
		return []byte(sb.String())
	}

	if msg.BodyHTML != "" && msg.BodyText != "" {
		altBoundary := s.generateBoundary()
		sb.WriteString("Content-Type: multipart/alternative; boundary=\"")
		sb.WriteString(altBoundary)
		sb.WriteString("\"\r\n\r\n")

		s.writeAlternativeParts(&sb, altBoundary, msg)

		sb.WriteString("--")
		sb.WriteString(altBoundary)
		sb.WriteString("--\r\n")
		return []byte(sb.String())
	}

	if msg.BodyHTML != "" {
		sb.WriteString("Content-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(msg.BodyHTML)
		return []byte(sb.String())
	}

	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	sb.WriteString(msg.BodyText)
	return []byte(sb.String())
}

func (s *SMTPSender) writeAlternativeParts(sb *strings.Builder, boundary string, msg *Message) {
	if msg.BodyText != "" {
		sb.WriteString("--")
		sb.WriteString(boundary)
		sb.WriteString("\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(msg.BodyText)
		sb.WriteString("\r\n")
	}

	if msg.BodyHTML != "" {
		sb.WriteString("--")
		sb.WriteString(boundary)
		sb.WriteString("\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(msg.BodyHTML)
		sb.WriteString("\r\n")
	}
}

func (s *SMTPSender) writeAttachmentPart(sb *strings.Builder, att Attachment) {
	mimeType := att.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	encodedFilename := encodeMIMEHeader(att.Filename)

	sb.WriteString("Content-Type: ")
	sb.WriteString(mimeType)
	sb.WriteString("; name=\"")
	sb.WriteString(encodedFilename)
	sb.WriteString("\"\r\nContent-Disposition: attachment; filename=\"")
	sb.WriteString(encodedFilename)
	sb.WriteString("\"\r\nContent-Transfer-Encoding: base64\r\n\r\n")

	raw := att.ContentBase64
	for len(raw) > 76 {
		sb.WriteString(raw[:76])
		sb.WriteString("\r\n")
		raw = raw[76:]
	}
	sb.WriteString(raw)
	sb.WriteString("\r\n")
}
