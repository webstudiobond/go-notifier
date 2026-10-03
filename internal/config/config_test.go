package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if content == "" {
		return
	}
	err := os.WriteFile(filepath.Clean(filepath.Join(dir, name)), []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to write test file %s: %v", name, err)
	}
}

func verifySiteAndSMTP(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.SiteName != "Test Site" {
		t.Errorf("expected SiteName 'Test Site', got %s", cfg.SiteName)
	}
	if cfg.SMTP.Host != "mail.example.com" || cfg.SMTP.Port != 465 {
		t.Errorf("unexpected SMTP host/port: %+v", cfg.SMTP)
	}
	if !cfg.SMTP.Enabled || !cfg.SMTPEnabled || !cfg.AdminFilterRequired {
		t.Errorf("unexpected SMTP enabled flags: %+v", cfg.SMTP)
	}
}

func verifyLoadedConfig(t *testing.T, cfg *Config) {
	t.Helper()
	verifySiteAndSMTP(t, cfg)
	if !cfg.Telegram.Enabled || cfg.Telegram.ChatID != "-1001234567890" {
		t.Errorf("unexpected Telegram config: %+v", cfg.Telegram)
	}
	if !cfg.Matrix.Enabled || cfg.Matrix.RoomID != "!roomid:example.com" {
		t.Errorf("unexpected Matrix config: %+v", cfg.Matrix)
	}
	if !cfg.Ntfy.Enabled || cfg.Ntfy.Topic != "alerts" {
		t.Errorf("unexpected Ntfy config: %+v", cfg.Ntfy)
	}
	if len(cfg.AdminEmails) != 2 {
		t.Errorf("expected 2 admin emails, got %v", cfg.AdminEmails)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(cfg.Routes))
	}
	if cfg.Routes[0].Name != "WORDFENCE" || !cfg.Routes[0].SendAttachments {
		t.Errorf("unexpected route content: %+v", cfg.Routes[0])
	}
}

func TestLoadConfig_Success(t *testing.T) {
	mandatoryDir := t.TempDir()
	optionalDir := t.TempDir()

	testFiles := []struct {
		dir  string
		name string
		data string
	}{
		{mandatoryDir, "smtp_host", "mail.example.com"},
		{mandatoryDir, "smtp_port", "465"},
		{mandatoryDir, "smtp_mail", "noreply@example.com"},
		{mandatoryDir, "smtp_password", "initialpass1"},
		{optionalDir, "telegram_bot_token.txt", "123456789:ABCdefGhIjkLmNoPqRsTuVwXyZ"},
		{optionalDir, "telegram_chat_id.txt", "-1001234567890"},
		{optionalDir, "matrix_url.txt", "https://matrix.example.com"},
		{optionalDir, "matrix_room_id.txt", "!roomid:example.com"},
		{optionalDir, "matrix_access_token.txt", "matrixtoken"},
		{optionalDir, "ntfy_url.txt", "https://ntfy.example.org"},
		{optionalDir, "ntfy_topic.txt", "alerts"},
		{optionalDir, "ntfy_token.txt", "ntfysecret"},
		{optionalDir, "admin_emails.txt", "admin@example.com, security@example.org"},
	}

	for _, f := range testFiles {
		writeTestFile(t, f.dir, f.name, f.data)
	}

	t.Setenv("SITE_NAME", "Test Site")
	t.Setenv("SMTP_FROM_NAME", "Support Team")
	t.Setenv("NOTIFY_RATE_LIMIT_PER_MINUTE", "20")
	t.Setenv("NOTIFY_BURST", "10")
	t.Setenv("NOTIFY_MAX_ATTACHMENT_SIZE_MB", "15")
	t.Setenv("NOTIFY_RULE_WORDFENCE_MATCH", "Wordfence|Security")
	t.Setenv("NOTIFY_RULE_WORDFENCE_TARGETS", "telegram,smtp")
	t.Setenv("NOTIFY_RULE_WORDFENCE_ATTACHMENTS", "true")

	cfg, err := LoadConfig(mandatoryDir, optionalDir)
	if err != nil {
		t.Fatalf("LoadConfig unexpected error: %v", err)
	}

	verifyLoadedConfig(t, cfg)
}

func TestLoadConfig_MandatoryValidationFailures(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     string
		mail     string
		pass     string
		errMatch bool
	}{
		{"missing_host", "", "465", "user1@example.com", "pass1", true},
		{"invalid_port_str", "smtp.example.net", "abc", "user2@example.net", "pass2", true},
		{"unsupported_port", "relay.example.org", "2525", "user3@example.org", "pass3", true},
		{"invalid_email", "mx.example.com", "587", "invalid-email", "pass4", true},
		{"missing_password", "mail.example.net", "465", "user5@example.net", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mDir := t.TempDir()
			oDir := t.TempDir()

			writeTestFile(t, mDir, "smtp_host", tt.host)
			writeTestFile(t, mDir, "smtp_port", tt.port)
			writeTestFile(t, mDir, "smtp_mail", tt.mail)
			writeTestFile(t, mDir, "smtp_password", tt.pass)

			_, err := LoadConfig(mDir, oDir)
			if (err != nil) != tt.errMatch {
				t.Fatalf("expected error: %v, got: %v", tt.errMatch, err)
			}
		})
	}
}

type optionalFailureCase struct {
	name        string
	tgToken     string
	tgChatID    string
	matrixURL   string
	matrixRoom  string
	matrixToken string
	ntfyURL     string
	ntfyTopic   string
	shouldFail  bool
}

func getOptionalFailureCases() []optionalFailureCase {
	return []optionalFailureCase{
		{
			name:       "invalid_telegram_token",
			tgToken:    "invalidtoken",
			tgChatID:   "12345",
			shouldFail: true,
		},
		{
			name:       "invalid_telegram_chat_id",
			tgToken:    "123456:ABCdefGhIjkLmNoPqRsTuVwXyZ",
			tgChatID:   "not-a-number",
			shouldFail: true,
		},
		{
			name:        "invalid_matrix_url",
			matrixURL:   "ftp://matrix.example.com",
			matrixRoom:  "!room:example.com",
			matrixToken: "token",
			shouldFail:  true,
		},
		{
			name:        "invalid_matrix_room",
			matrixURL:   "https://matrix.example.com",
			matrixRoom:  "room_without_exclamation",
			matrixToken: "token",
			shouldFail:  true,
		},
		{
			name:       "invalid_ntfy_url",
			ntfyURL:    "://invalid-url",
			ntfyTopic:  "topic",
			shouldFail: true,
		},
		{
			name:       "invalid_ntfy_topic",
			ntfyURL:    "https://ntfy.example.org",
			ntfyTopic:  "invalid topic spaces!",
			shouldFail: true,
		},
	}
}

func TestLoadConfig_OptionalValidationFailures(t *testing.T) {
	tests := getOptionalFailureCases()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mDir := t.TempDir()
			oDir := t.TempDir()

			writeTestFile(t, mDir, "smtp_host", "gateway.example.com")
			writeTestFile(t, mDir, "smtp_port", "465")
			writeTestFile(t, mDir, "smtp_mail", "noreply@sub.example.com")
			writeTestFile(t, mDir, "smtp_password", "strongsecret")

			writeTestFile(t, oDir, "telegram_bot_token.txt", tt.tgToken)
			writeTestFile(t, oDir, "telegram_chat_id.txt", tt.tgChatID)
			writeTestFile(t, oDir, "matrix_url.txt", tt.matrixURL)
			writeTestFile(t, oDir, "matrix_room_id.txt", tt.matrixRoom)
			writeTestFile(t, oDir, "matrix_access_token.txt", tt.matrixToken)
			writeTestFile(t, oDir, "ntfy_url.txt", tt.ntfyURL)
			writeTestFile(t, oDir, "ntfy_topic.txt", tt.ntfyTopic)

			_, err := LoadConfig(mDir, oDir)
			if (err != nil) != tt.shouldFail {
				t.Fatalf("expected failure %v, got %v", tt.shouldFail, err)
			}
		})
	}
}

func TestParseEmails(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"admin@example.com", []string{"admin@example.com"}},
		{"a@example.com, b@example.net c@example.org;\td@example.com\n", []string{"a@example.com", "b@example.net", "c@example.org", "d@example.com"}},
	}

	for _, tt := range tests {
		res := parseEmails(tt.input)
		if !reflect.DeepEqual(res, tt.expected) {
			t.Errorf("parseEmails(%q) = %v, expected %v", tt.input, res, tt.expected)
		}
	}
}

func TestValidateRoomID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid_classic_format", "!room:example.com", false},
		{"valid_domain_less_v12", "!0xRqYq5IIruJFFcCLhkzepUfk5m2InboNUkXe3ZTqPs", false},
		{"valid_classic_with_port", "!room:example.net:8448", false},
		{"missing_exclamation_prefix", "room:example.org", true},
		{"room_alias_rejected", "#room:example.com", true},
		{"empty_identifier_body", "!", true},
		{"empty_localpart", "!:example.com", true},
		{"empty_server_name", "!room:", true},
		{"invalid_local_characters", "!ro*om:example.net", true},
		{"invalid_server_characters", "!room:ex@mple.org", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoomID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRoomID(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid_https_domain", "https://example.com", false},
		{"valid_https_domain_port", "https://matrix.example.net:8448", false},
		{"valid_http_ipv4_port", "http://127.0.0.1:8008", false},
		{"valid_http_ipv6_port", "http://[2001:db8::1]:8008", false},
		{"valid_single_label_internal", "http://synapse:8008", false},
		{"invalid_scheme", "ftp://example.org", true},
		{"invalid_structure", "://invalid-url", true},
		{"missing_host", "http://", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateURL(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig_SMTPDisabled_WithoutSecrets(t *testing.T) {
	mandatoryDir := t.TempDir()
	optionalDir := t.TempDir()

	writeTestFile(t, optionalDir, "telegram_bot_token.txt", "123456789:ABCdefGhIjkLmNoPqRsTuVwXyZ")
	writeTestFile(t, optionalDir, "telegram_chat_id.txt", "-1001234567890")

	t.Setenv("NOTIFY_SMTP_ENABLED", "false")

	cfg, err := LoadConfig(mandatoryDir, optionalDir)
	if err != nil {
		t.Fatalf("LoadConfig unexpected error: %v", err)
	}

	if cfg.SMTPEnabled || cfg.SMTP.Enabled {
		t.Errorf("expected SMTP to be disabled, got SMTPEnabled=%v, SMTP.Enabled=%v", cfg.SMTPEnabled, cfg.SMTP.Enabled)
	}
	if !cfg.Telegram.Enabled {
		t.Errorf("expected Telegram to be enabled")
	}
	if len(cfg.DefaultChannels) != 1 || cfg.DefaultChannels[0] != "telegram" {
		t.Errorf("expected default channels [telegram], got %v", cfg.DefaultChannels)
	}
}

func TestLoadConfig_SMTPDisabled_IgnoresSecrets(t *testing.T) {
	mandatoryDir := t.TempDir()
	optionalDir := t.TempDir()

	writeTestFile(t, mandatoryDir, "smtp_host", "mail.example.net")
	writeTestFile(t, mandatoryDir, "smtp_port", "465")
	writeTestFile(t, mandatoryDir, "smtp_mail", "bot@example.net")
	writeTestFile(t, mandatoryDir, "smtp_password", "supersecret")

	writeTestFile(t, optionalDir, "telegram_bot_token.txt", "987654321:ZYXwvUtSrQpOnMlKjIhGfEdCbA")
	writeTestFile(t, optionalDir, "telegram_chat_id.txt", "-1009876543210")

	t.Setenv("NOTIFY_SMTP_ENABLED", "false")

	cfg, err := LoadConfig(mandatoryDir, optionalDir)
	if err != nil {
		t.Fatalf("LoadConfig unexpected error: %v", err)
	}

	if cfg.SMTPEnabled || cfg.SMTP.Enabled {
		t.Errorf("expected SMTP to be disabled despite present secrets")
	}
	if cfg.SMTP.Host != "" || cfg.SMTP.Mail != "" {
		t.Errorf("expected SMTP credentials to be empty, got %+v", cfg.SMTP)
	}
}

func TestLoadConfig_SMTPDisabled_ExplicitSMTPChannel_Fails(t *testing.T) {
	tests := []struct {
		name       string
		channels   string
		ruleMatch  string
		ruleTarget string
	}{
		{
			name:     "explicit_default_channel_smtp",
			channels: "smtp",
		},
		{
			name:       "explicit_rule_target_smtp",
			ruleMatch:  "Critical",
			ruleTarget: "smtp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mandatoryDir := t.TempDir()
			optionalDir := t.TempDir()

			writeTestFile(t, optionalDir, "telegram_bot_token.txt", "111222333:AAAbbbCCCdddeeefff")
			writeTestFile(t, optionalDir, "telegram_chat_id.txt", "-1001112223334")

			t.Setenv("NOTIFY_SMTP_ENABLED", "false")
			if tt.channels != "" {
				t.Setenv("NOTIFY_CHANNELS", tt.channels)
			}
			if tt.ruleMatch != "" {
				t.Setenv("NOTIFY_RULE_ALERT_MATCH", tt.ruleMatch)
				t.Setenv("NOTIFY_RULE_ALERT_TARGETS", tt.ruleTarget)
			}

			_, err := LoadConfig(mandatoryDir, optionalDir)
			if !errors.Is(err, ErrSMTPDisabledExplicit) {
				t.Fatalf("expected ErrSMTPDisabledExplicit, got %v", err)
			}
		})
	}
}

func TestLoadConfig_UnconfiguredChannel_Fails(t *testing.T) {
	tests := []struct {
		expectedErr error
		name        string
		channel     string
		errContains string
	}{
		{
			name:        "unconfigured_telegram",
			channel:     "telegram",
			expectedErr: ErrTelegramNotConfigured,
		},
		{
			name:        "unconfigured_matrix",
			channel:     "matrix",
			expectedErr: ErrMatrixNotConfigured,
		},
		{
			name:        "unconfigured_ntfy",
			channel:     "ntfy",
			expectedErr: ErrNtfyNotConfigured,
		},
		{
			name:        "unknown_channel_slack",
			channel:     "slack",
			errContains: "unknown notification channel",
		},
		{
			name:        "unknown_channel_webhook",
			channel:     "webhook",
			errContains: "unknown notification channel",
		},
	}

	for _, tt := range tests {
		for _, isRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_isRule_%v", tt.name, isRule), func(t *testing.T) {
				mandatoryDir := t.TempDir()
				optionalDir := t.TempDir()

				writeTestFile(t, mandatoryDir, "smtp_host", "mail.example.org")
				writeTestFile(t, mandatoryDir, "smtp_port", "587")
				writeTestFile(t, mandatoryDir, "smtp_mail", "admin@example.org")
				writeTestFile(t, mandatoryDir, "smtp_password", "passphrase")

				if isRule {
					t.Setenv("NOTIFY_RULE_TEST_MATCH", "Security")
					t.Setenv("NOTIFY_RULE_TEST_TARGETS", tt.channel)
				} else {
					t.Setenv("NOTIFY_CHANNELS", tt.channel)
				}

				_, err := LoadConfig(mandatoryDir, optionalDir)
				if tt.expectedErr != nil && !errors.Is(err, tt.expectedErr) {
					t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
				}
				if tt.errContains != "" && (err == nil || !strings.Contains(err.Error(), tt.errContains)) {
					t.Fatalf("expected error containing %q, got %v", tt.errContains, err)
				}
			})
		}
	}
}

func TestLoadConfig_ZeroChannelsConfigured_Fails(t *testing.T) {
	mandatoryDir := t.TempDir()
	optionalDir := t.TempDir()

	t.Setenv("NOTIFY_SMTP_ENABLED", "false")

	_, err := LoadConfig(mandatoryDir, optionalDir)
	if !errors.Is(err, ErrZeroChannelsConfigured) {
		t.Fatalf("expected ErrZeroChannelsConfigured, got %v", err)
	}
}

func TestLoadConfig_AdminFilterFlag(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		expected bool
	}{
		{"default_when_unset", "", true},
		{"explicit_true", "true", true},
		{"explicit_false", "false", false},
		{"fallback_on_invalid", "not-a-bool", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mandatoryDir := t.TempDir()
			optionalDir := t.TempDir()

			writeTestFile(t, mandatoryDir, "smtp_host", "mail.example.com")
			writeTestFile(t, mandatoryDir, "smtp_port", "465")
			writeTestFile(t, mandatoryDir, "smtp_mail", "test@example.com")
			writeTestFile(t, mandatoryDir, "smtp_password", "secret")

			if tt.envVal != "" {
				t.Setenv("NOTIFY_ADMIN_FILTER_REQUIRED", tt.envVal)
			}

			cfg, err := LoadConfig(mandatoryDir, optionalDir)
			if err != nil {
				t.Fatalf("LoadConfig unexpected error: %v", err)
			}
			if cfg.AdminFilterRequired != tt.expected {
				t.Errorf("expected AdminFilterRequired=%v, got %v", tt.expected, cfg.AdminFilterRequired)
			}
		})
	}
}

func TestReadSecretFiles_EdgeCases(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "regular.txt", "content-one")
	val, err := readSecretFile(dir, "regular.txt")
	if err != nil || val != "content-one" {
		t.Fatalf("unexpected readSecretFile result: %q, %v", val, err)
	}

	subDir := filepath.Join(dir, "is_a_dir")
	if mkErr := os.Mkdir(subDir, 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := readSecretBytes(dir, "is_a_dir"); err == nil {
		t.Fatal("expected error reading directory as secret")
	}

	altSubDir := filepath.Join(dir, "fallback_dir.txt")
	if mkErr := os.Mkdir(altSubDir, 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := readSecretBytes(dir, "fallback_dir"); err == nil {
		t.Fatal("expected error reading alt directory as secret")
	}

	if _, err := readSecretBytes(dir, "completely_missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}

	optVal, optErr := readOptionalSecretFile(dir, "missing_optional")
	if optErr != nil || optVal != "" {
		t.Fatalf("expected empty result for missing optional, got %q, %v", optVal, optErr)
	}

	if _, err := readOptionalSecretFile(dir, "is_a_dir"); err == nil {
		t.Fatal("expected error reading optional directory as file")
	}
}

func TestParsePositiveInt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		fallback int
		expected int
	}{
		{"empty_input", "", 10, 10},
		{"negative_input", "-5", 10, 10},
		{"invalid_non_numeric", "not_a_number", 15, 15},
		{"valid_positive", "42", 10, 42},
		{"valid_zero", "0", 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePositiveInt(tt.input, tt.fallback)
			if got != tt.expected {
				t.Fatalf("parsePositiveInt(%q, %d) = %d, want %d", tt.input, tt.fallback, got, tt.expected)
			}
		})
	}
}

func TestDomainValidation_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"valid_localhost", "localhost", false},
		{"valid_with_trailing_dot", "valid.example.com.", false},
		{"empty_domain", "", true},
		{"too_long_domain", strings.Repeat("a.", 130) + "com", true},
		{"numeric_tld", "server.123", true},
		{"numeric_tld_multiple_parts", "api.sub.456", true},
		{"valid_non_numeric_tld", "server.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDomain(tt.domain)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDomain(%q) error = %v, wantErr = %v", tt.domain, err, tt.wantErr)
			}
		})
	}
}

func TestDomainLabelValidation_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		label   string
		wantErr bool
	}{
		{"valid_label", "example-one", false},
		{"empty_label", "", true},
		{"too_long_label", strings.Repeat("a", 64), true},
		{"starts_with_hyphen", "-start", true},
		{"ends_with_hyphen", "end-", true},
		{"invalid_characters", "inv@lid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDomainLabel(tt.label)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDomainLabel(%q) error = %v, wantErr = %v", tt.label, err, tt.wantErr)
			}
		})
	}
}

func TestValidateServerName_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		wantErr    bool
	}{
		{"valid_domain_only", "api.example.org", false},
		{"valid_domain_with_port", "mail.example.net:587", false},
		{"valid_ipv4_with_port", "192.0.2.1:8080", false},
		{"valid_ipv6_with_port", "[2001:db8::1]:8448", false},
		{"port_out_of_range_low", "mail.example.org:0", true},
		{"port_out_of_range_high", "mail.example.org:65536", true},
		{"port_non_numeric", "mail.example.org:portnum", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServerName(tt.serverName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateServerName(%q) error = %v, wantErr = %v", tt.serverName, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSMTP_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SMTPConfig
		wantErr bool
	}{
		{
			name: "empty_host",
			cfg: SMTPConfig{
				Host:     "",
				Port:     465,
				Mail:     "test@example.com",
				Password: "secretpass_alpha",
			},
			wantErr: true,
		},
		{
			name: "valid_port_25",
			cfg: SMTPConfig{
				Host:     "relay.example.com",
				Port:     25,
				Mail:     "test@example.com",
				Password: "secretpass_beta",
			},
			wantErr: false,
		},
		{
			name: "valid_port_587",
			cfg: SMTPConfig{
				Host:     "submission.example.net",
				Port:     587,
				Mail:     "user@example.net",
				Password: "secretpass_gamma",
			},
			wantErr: false,
		},
		{
			name: "empty_password",
			cfg: SMTPConfig{
				Host:     "mail.example.org",
				Port:     465,
				Mail:     "user@example.org",
				Password: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSMTP(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSMTP() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMatrix_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MatrixConfig
		wantErr bool
	}{
		{
			name:    "disabled_matrix_is_valid",
			cfg:     MatrixConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "enabled_missing_access_token",
			cfg: MatrixConfig{
				Enabled:     true,
				URL:         "https://matrix.example.net",
				RoomID:      "!room_unique:example.org",
				AccessToken: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMatrix(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMatrix() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveSiteName(t *testing.T) {
	tests := []struct {
		name     string
		siteName string
		siteUser string
		servName string
		expected string
	}{
		{"from_site_name", "Primary Name", "User Name", "Server Host", "Primary Name"},
		{"fallback_to_site_user", "", "Secondary User", "Server Host", "Secondary User"},
		{"fallback_to_server_name", "", "", "Tertiary Server", "Tertiary Server"},
		{"fallback_to_default", "", "", "", "WordPress Site"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SITE_NAME", tt.siteName)
			t.Setenv("SITE_USER", tt.siteUser)
			t.Setenv("SERVER_NAME", tt.servName)

			got := resolveSiteName()
			if got != tt.expected {
				t.Fatalf("resolveSiteName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestResolveAdminEmails(t *testing.T) {
	tests := []struct {
		name        string
		fileData    string
		envData     string
		expectedLen int
		makeDirFile bool
		wantErr     bool
	}{
		{
			name:        "from_file",
			fileData:    "admin1@example.com, admin2@example.org",
			envData:     "",
			expectedLen: 2,
			makeDirFile: false,
			wantErr:     false,
		},
		{
			name:        "fallback_to_env",
			fileData:    "",
			envData:     "admin-env@example.net",
			expectedLen: 1,
			makeDirFile: false,
			wantErr:     false,
		},
		{
			name:        "file_read_error_on_directory",
			fileData:    "",
			envData:     "",
			expectedLen: 0,
			makeDirFile: true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.makeDirFile {
				if mkErr := os.Mkdir(filepath.Join(dir, "admin_emails"), 0o700); mkErr != nil {
					t.Fatalf("mkdir failed: %v", mkErr)
				}
			} else if tt.fileData != "" {
				writeTestFile(t, dir, "admin_emails.txt", tt.fileData)
			}

			t.Setenv("NOTIFY_ADMIN_EMAILS", tt.envData)

			emails, err := resolveAdminEmails(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveAdminEmails() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(emails) != tt.expectedLen {
				t.Fatalf("expected %d emails, got %v", tt.expectedLen, emails)
			}
		})
	}
}

func TestLoadSMTP_ReadErrors(t *testing.T) {
	mDir := t.TempDir()
	writeTestFile(t, mDir, "smtp_host", "mail.example.com")
	if mkErr := os.Mkdir(filepath.Join(mDir, "smtp_port"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadSMTP(mDir, true); err == nil {
		t.Fatal("expected error reading smtp_port directory")
	}

	mDir2 := t.TempDir()
	writeTestFile(t, mDir2, "smtp_host", "mail.example.com")
	writeTestFile(t, mDir2, "smtp_port", "465")
	if mkErr := os.Mkdir(filepath.Join(mDir2, "smtp_mail"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadSMTP(mDir2, true); err == nil {
		t.Fatal("expected error reading smtp_mail directory")
	}

	mDir3 := t.TempDir()
	writeTestFile(t, mDir3, "smtp_host", "mail.example.com")
	writeTestFile(t, mDir3, "smtp_port", "465")
	writeTestFile(t, mDir3, "smtp_mail", "user@example.com")
	if mkErr := os.Mkdir(filepath.Join(mDir3, "smtp_password"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadSMTP(mDir3, true); err == nil {
		t.Fatal("expected error reading smtp_password directory")
	}
}

func TestLoadTelegram_ReadErrorsAndFallback(t *testing.T) {
	oDir := t.TempDir()
	writeTestFile(t, oDir, "telegram_token.txt", "123456:AAAbbbCCCdddeeefff")
	writeTestFile(t, oDir, "telegram_chat_id.txt", "-100123456")
	tg, err := loadTelegram(oDir)
	if err != nil {
		t.Fatalf("unexpected loadTelegram error: %v", err)
	}
	if !tg.Enabled || tg.Token != "123456:AAAbbbCCCdddeeefff" {
		t.Fatalf("expected enabled telegram with token, got %+v", tg)
	}

	oDirErr1 := t.TempDir()
	if mkErr := os.Mkdir(filepath.Join(oDirErr1, "telegram_token"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadTelegram(oDirErr1); err == nil {
		t.Fatal("expected error reading telegram_token directory")
	}

	oDirErr2 := t.TempDir()
	if mkErr := os.Mkdir(filepath.Join(oDirErr2, "telegram_bot_token"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadTelegram(oDirErr2); err == nil {
		t.Fatal("expected error reading telegram_bot_token directory")
	}

	oDirErr3 := t.TempDir()
	writeTestFile(t, oDirErr3, "telegram_token.txt", "123456:AAAbbbCCCdddeeefff")
	if mkErr := os.Mkdir(filepath.Join(oDirErr3, "telegram_chat_id"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadTelegram(oDirErr3); err == nil {
		t.Fatal("expected error reading telegram_chat_id directory")
	}
}

func TestLoadMatrix_ReadErrors(t *testing.T) {
	oDir := t.TempDir()
	if mkErr := os.Mkdir(filepath.Join(oDir, "matrix_url"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadMatrix(oDir); err == nil {
		t.Fatal("expected error reading matrix_url directory")
	}

	oDir2 := t.TempDir()
	writeTestFile(t, oDir2, "matrix_url.txt", "https://matrix.example.org")
	if mkErr := os.Mkdir(filepath.Join(oDir2, "matrix_room_id"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadMatrix(oDir2); err == nil {
		t.Fatal("expected error reading matrix_room_id directory")
	}

	oDir3 := t.TempDir()
	writeTestFile(t, oDir3, "matrix_url.txt", "https://matrix.example.org")
	writeTestFile(t, oDir3, "matrix_room_id.txt", "!room:example.org")
	if mkErr := os.Mkdir(filepath.Join(oDir3, "matrix_access_token"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadMatrix(oDir3); err == nil {
		t.Fatal("expected error reading matrix_access_token directory")
	}
}

func TestLoadNtfy_ReadErrors(t *testing.T) {
	oDir := t.TempDir()
	if mkErr := os.Mkdir(filepath.Join(oDir, "ntfy_url"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadNtfy(oDir); err == nil {
		t.Fatal("expected error reading ntfy_url directory")
	}

	oDir2 := t.TempDir()
	writeTestFile(t, oDir2, "ntfy_url.txt", "https://ntfy.example.net")
	if mkErr := os.Mkdir(filepath.Join(oDir2, "ntfy_topic"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadNtfy(oDir2); err == nil {
		t.Fatal("expected error reading ntfy_topic directory")
	}

	oDir3 := t.TempDir()
	writeTestFile(t, oDir3, "ntfy_url.txt", "https://ntfy.example.net")
	writeTestFile(t, oDir3, "ntfy_topic.txt", "alerts-unique")
	if mkErr := os.Mkdir(filepath.Join(oDir3, "ntfy_token"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}
	if _, err := loadNtfy(oDir3); err == nil {
		t.Fatal("expected error reading ntfy_token directory")
	}
}

func TestParseChannels_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		expectedLen int
	}{
		{"only_commas_and_spaces", ", , , ", 2},
		{"mixed_valid_and_whitespace", " smtp , telegram ", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseChannels(tt.raw, true, true, false, false)
			if err != nil {
				t.Fatalf("parseChannels(%q) unexpected error: %v", tt.raw, err)
			}
			if len(res) != tt.expectedLen {
				t.Fatalf("parseChannels(%q) expected len %d, got %v", tt.raw, tt.expectedLen, res)
			}
		})
	}
}

func TestRoutes_EdgeCases(t *testing.T) {
	t.Run("empty_route_targets", func(t *testing.T) {
		targets, err := parseRouteTargets("", true, true, false, false)
		if err != nil || targets != nil {
			t.Fatalf("expected nil targets for empty raw, got %v, %v", targets, err)
		}
	})

	t.Run("invalid_match_regex", func(t *testing.T) {
		_, err := parseSingleRoute("NOTIFY_RULE_BAD_MATCH", "[unclosed", true, true, false, false)
		if err == nil {
			t.Fatal("expected error for unclosed regex")
		}
	})

	t.Run("parse_routes_invalid_regex_fails", func(t *testing.T) {
		t.Setenv("NOTIFY_RULE_BROKEN_MATCH", "(unclosed")
		_, err := parseRoutes(true, true, false, false)
		if err == nil {
			t.Fatal("expected parseRoutes error on invalid regex env var")
		}
	})

	t.Run("parse_routes_malformed_environ_ignored", func(t *testing.T) {
		origEnviron := osEnviron
		defer func() { osEnviron = origEnviron }()
		osEnviron = mockMalformedEnviron

		routes, err := parseRoutes(true, true, false, false)
		if err != nil || len(routes) != 0 {
			t.Fatalf("unexpected parseRoutes result: %v, %v", routes, err)
		}
	})
}

func mockMalformedEnviron() []string {
	return []string{"MALFORMED_ENTRY_NO_EQUALS"}
}

func TestLoadConfig_SocketPathCustom(t *testing.T) {
	mDir := t.TempDir()
	oDir := t.TempDir()

	writeTestFile(t, mDir, "smtp_host", "mail.example.com")
	writeTestFile(t, mDir, "smtp_port", "465")
	writeTestFile(t, mDir, "smtp_mail", "test@example.com")
	writeTestFile(t, mDir, "smtp_password", "pass")

	customSocket := "/tmp/custom_notify.sock"
	t.Setenv("NOTIFY_SOCKET_PATH", customSocket)

	cfg, err := LoadConfig(mDir, oDir)
	if err != nil {
		t.Fatalf("LoadConfig unexpected error: %v", err)
	}
	if cfg.SocketPath != customSocket {
		t.Errorf("expected SocketPath=%q, got %q", customSocket, cfg.SocketPath)
	}
}

func TestLoadConfig_ResolveAdminEmailsError_Fails(t *testing.T) {
	mDir := t.TempDir()
	oDir := t.TempDir()

	writeTestFile(t, mDir, "smtp_host", "mail.example.com")
	writeTestFile(t, mDir, "smtp_port", "465")
	writeTestFile(t, mDir, "smtp_mail", "test@example.com")
	writeTestFile(t, mDir, "smtp_password", "pass")

	if mkErr := os.Mkdir(filepath.Join(oDir, "admin_emails"), 0o700); mkErr != nil {
		t.Fatalf("mkdir failed: %v", mkErr)
	}

	_, err := LoadConfig(mDir, oDir)
	if err == nil {
		t.Fatal("expected LoadConfig to fail on admin_emails directory read error")
	}
}
