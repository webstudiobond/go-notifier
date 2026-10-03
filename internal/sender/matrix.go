package sender

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MatrixSender dispatches notifications and media events to Matrix homeservers.
type MatrixSender struct {
	client      *http.Client
	baseURL     string
	roomID      string
	accessToken string
	siteName    string
}

type matrixTextMessage struct {
	MsgType       string `json:"msgtype"`
	Body          string `json:"body"`
	Format        string `json:"format,omitempty"`
	FormattedBody string `json:"formatted_body,omitempty"`
}

type matrixFileInfo struct {
	MIMEType string `json:"mimetype"`
	Size     int    `json:"size"`
}

type matrixFileMessage struct {
	MsgType  string         `json:"msgtype"`
	Body     string         `json:"body"`
	Filename string         `json:"filename"`
	URL      string         `json:"url"`
	Info     matrixFileInfo `json:"info"`
}

type matrixUploadResponse struct {
	ContentURI string `json:"content_uri"`
}

// NewMatrixSender creates an initialized MatrixSender driver.
func NewMatrixSender(baseURL, roomID, accessToken, siteName string) *MatrixSender {
	return &MatrixSender{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:     strings.TrimRight(baseURL, "/"),
		roomID:      roomID,
		accessToken: accessToken,
		siteName:    siteName,
	}
}

// Name returns the driver identifier.
func (s *MatrixSender) Name() string {
	return "matrix"
}

// Send formats and delivers text messages and optional files to Matrix.
func (s *MatrixSender) Send(ctx context.Context, msg *Message, sendAttachments bool) error {
	plain, formatted := s.formatMessage(msg)
	if err := s.sendTextMessage(ctx, plain, formatted); err != nil {
		return err
	}

	if sendAttachments && len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			if err := s.sendFile(ctx, att); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MatrixSender) formatMessage(msg *Message) (plain, formatted string) {
	var plainBuilder strings.Builder
	plainBuilder.WriteString("🔐 Notification: ")
	plainBuilder.WriteString(s.siteName)
	plainBuilder.WriteString("\n\nSubject: ")
	plainBuilder.WriteString(msg.Subject)
	plainBuilder.WriteString("\n\n")

	content := msg.BodyText
	if content == "" {
		content = msg.BodyHTML
	}
	plainBuilder.WriteString(content)

	var fmtBuilder strings.Builder
	fmtBuilder.WriteString("<strong>🔐 Notification: ")
	fmtBuilder.WriteString(html.EscapeString(s.siteName))
	fmtBuilder.WriteString("</strong><br><br><strong>Subject:</strong> ")
	fmtBuilder.WriteString(html.EscapeString(msg.Subject))
	fmtBuilder.WriteString("<br><br>")

	if msg.BodyHTML != "" {
		fmtBuilder.WriteString(msg.BodyHTML)
	} else {
		escaped := html.EscapeString(content)
		fmtBuilder.WriteString(strings.ReplaceAll(escaped, "\n", "<br>"))
	}

	return plainBuilder.String(), fmtBuilder.String()
}

func (s *MatrixSender) generateTxnID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func (s *MatrixSender) sendTextMessage(ctx context.Context, plain, formatted string) error {
	txnID := s.generateTxnID()
	encodedRoom := url.PathEscape(s.roomID)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", s.baseURL, encodedRoom, txnID)

	payload := matrixTextMessage{
		MsgType:       "m.text",
		Body:          plain,
		Format:        "org.matrix.custom.html",
		FormattedBody: formatted,
	}

	raw, err := jsonMarshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix message: %w", err)
	}

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("create matrix request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

func (s *MatrixSender) uploadMedia(ctx context.Context, att Attachment, data []byte) (string, error) {
	encodedFilename := url.QueryEscape(att.Filename)
	endpoint := fmt.Sprintf("%s/_matrix/media/v3/upload?filename=%s", s.baseURL, encodedFilename)
	mimeType := att.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	var contentURI string
	err := s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("create matrix media upload request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
		req.Header.Set("Content-Type", mimeType)
		return req, nil
	}, func(body []byte) error {
		var uploadResp matrixUploadResponse
		if err := json.Unmarshal(body, &uploadResp); err != nil {
			return fmt.Errorf("decode matrix upload response: %w", err)
		}
		if uploadResp.ContentURI == "" {
			return errors.New("matrix media upload response missing content_uri")
		}
		contentURI = uploadResp.ContentURI
		return nil
	})
	if err != nil {
		return "", err
	}
	return contentURI, nil
}

func (s *MatrixSender) sendFile(ctx context.Context, att Attachment) error {
	data, err := base64.StdEncoding.DecodeString(att.ContentBase64)
	if err != nil {
		return fmt.Errorf("decode matrix attachment %s: %w", att.Filename, err)
	}

	contentURI, err := s.uploadMedia(ctx, att, data)
	if err != nil {
		return err
	}

	txnID := s.generateTxnID()
	encodedRoom := url.PathEscape(s.roomID)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", s.baseURL, encodedRoom, txnID)

	payload := matrixFileMessage{
		MsgType:  "m.file",
		Body:     att.Filename,
		Filename: att.Filename,
		URL:      contentURI,
		Info: matrixFileInfo{
			MIMEType: att.MIMEType,
			Size:     len(data),
		},
	}

	raw, err := jsonMarshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix file message: %w", err)
	}

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("create matrix file event request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

func (s *MatrixSender) handleResponse(resp *http.Response, successHooks []func([]byte) error) error {
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		body = []byte("unable to read response")
	}

	closeErr := resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		for _, hook := range successHooks {
			if hookErr := hook(body); hookErr != nil {
				return hookErr
			}
		}
		if closeErr != nil {
			return fmt.Errorf("close matrix response: %w", closeErr)
		}
		return nil
	}

	if closeErr != nil {
		return fmt.Errorf("matrix returned status %d: %s (close: %w)", resp.StatusCode, string(body), closeErr)
	}
	return fmt.Errorf("matrix returned status %d: %s", resp.StatusCode, string(body))
}

func (s *MatrixSender) executeWithRetry(ctx context.Context, reqFactory func() (*http.Request, error), successHooks ...func([]byte) error) error {
	var lastErr error
	for attempt := range defaultMaxAttempts {
		req, err := reqFactory()
		if err != nil {
			return err
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("matrix request failed: %w", err)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(retryDelay(attempt)):
				continue
			}
		}

		resErr := s.handleResponse(resp, successHooks)
		if resErr == nil {
			return nil
		}
		lastErr = resErr

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(retryDelay(attempt)):
		}
	}

	if lastErr == nil {
		return errors.New("matrix request failed with unknown error")
	}
	return lastErr
}
