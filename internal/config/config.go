// Package config provides validation and loading of secrets and environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultSocketPath         = "/var/run/sockets/notify/notify.sock"
	defaultRateLimitPerMinute = 10
	defaultBurstLimit         = 5
	defaultMaxAttachmentMB    = 10

	telegramFormatPattern = `^\d+:[A-Za-z0-9_-]+$`
	ntfyTopicPattern      = `^[A-Za-z0-9_-]+$`
)

// SMTPConfig encapsulates mandatory credentials and transport settings for the relay.
type SMTPConfig struct {
	Host     string
	Mail     string
	Password string
	FromName string
	Port     int
	Enabled  bool
}

// TelegramConfig encapsulates credentials and target chat settings for Telegram.
type TelegramConfig struct {
	Token   string
	ChatID  string
	Enabled bool
}

// MatrixConfig encapsulates homeserver endpoints and credentials for Matrix.
type MatrixConfig struct {
	URL         string
	RoomID      string
	AccessToken string
	Enabled     bool
}

// NtfyConfig encapsulates server endpoints and credentials for ntfy.
type NtfyConfig struct {
	URL     string
	Topic   string
	Token   string
	Enabled bool
}

// RouteRule defines subject matching and target channel destinations.
type RouteRule struct {
	Name            string
	MatchRegex      *regexp.Regexp
	Targets         []string
	SendAttachments bool
}

// Config represents the complete runtime configuration parsed from secrets and environment.
type Config struct {
	SocketPath          string
	SiteName            string
	Ntfy                NtfyConfig
	Matrix              MatrixConfig
	Telegram            TelegramConfig
	Routes              []RouteRule
	DefaultChannels     []string
	AdminEmails         []string
	SMTP                SMTPConfig
	MaxAttachmentSizeMB int
	RateLimitPerMinute  int
	BurstLimit          int
	SMTPEnabled         bool
	AdminFilterRequired bool
}

// Configuration validation errors returned when mandatory channel credentials or prerequisites are missing.
var (
	ErrZeroChannelsConfigured = errors.New("no notification channels configured (SMTP and messengers are all disabled)")
	ErrSMTPDisabledExplicit   = errors.New("channel \"smtp\" cannot be configured when NOTIFY_SMTP_ENABLED=false (set NOTIFY_SMTP_ENABLED=true and provide SMTP secrets)")
	ErrTelegramNotConfigured  = errors.New("channel \"telegram\" is requested in targets but Telegram secrets are not configured")
	ErrMatrixNotConfigured    = errors.New("channel \"matrix\" is requested in targets but Matrix secrets are not configured")
	ErrNtfyNotConfigured      = errors.New("channel \"ntfy\" is requested in targets but ntfy secrets are not configured")
)

var (
	telegramTokenRegexp = regexp.MustCompile(telegramFormatPattern)
	ntfyTopicRegexp     = regexp.MustCompile(ntfyTopicPattern)
	osEnviron           = os.Environ
)

func readSecretBytes(dir, name string) ([]byte, error) {
	cleanPath := filepath.Clean(filepath.Join(dir, name))
	data, err := os.ReadFile(cleanPath)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret %s: %w", name, err)
	}

	altName, hasExt := strings.CutSuffix(name, ".txt")
	if !hasExt {
		altName = name + ".txt"
	}

	altPath := filepath.Clean(filepath.Join(dir, altName))
	altData, altErr := os.ReadFile(altPath)
	if altErr == nil {
		return altData, nil
	}
	if !errors.Is(altErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret %s: %w", altName, altErr)
	}

	return nil, fmt.Errorf("secret file %s: %w", name, err)
}

func readSecretFile(dir, name string) (string, error) {
	data, err := readSecretBytes(dir, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readOptionalSecretFile(dir, name string) (string, error) {
	data, err := readSecretBytes(dir, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func parseEmails(raw string) []string {
	if raw == "" {
		return nil
	}
	delims := func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t'
	}
	parts := strings.FieldsFunc(raw, delims)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parsePositiveInt(val string, fallback int) int {
	if val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func validateDomainLabel(part string) error {
	pl := len(part)
	if pl < 1 || pl > 63 {
		return errors.New("domain label length must be between 1 and 63")
	}
	if part[0] == '-' || part[pl-1] == '-' {
		return errors.New("domain label cannot start or end with hyphen")
	}
	for i := range len(part) {
		c := part[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return errors.New("invalid character in domain label")
		}
	}
	return nil
}

func isNumericLabel(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validateDomain(domain string) error {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "localhost" {
		return nil
	}
	domain = strings.TrimSuffix(domain, ".")
	if len(domain) < 1 || len(domain) > 253 {
		return errors.New("domain length must be between 1 and 253")
	}
	parts := strings.Split(domain, ".")
	for _, part := range parts {
		if err := validateDomainLabel(part); err != nil {
			return err
		}
	}
	if len(parts) >= 2 && isNumericLabel(parts[len(parts)-1]) {
		return errors.New("top-level domain cannot be purely numeric")
	}
	return nil
}

func validateServerName(name string) error {
	host, portStr, err := net.SplitHostPort(name)
	if err != nil {
		host = name
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	}

	cleanHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if net.ParseIP(cleanHost) != nil {
		return nil
	}

	return validateDomain(cleanHost)
}

func isValidRoomLocalpart(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '=' || c == '/' || c == '+' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validateRoomID(roomID string) error {
	if !strings.HasPrefix(roomID, "!") {
		return errors.New("matrix_room_id format is invalid: expected prefix '!'")
	}
	body := roomID[1:]
	if body == "" {
		return errors.New("matrix_room_id format is invalid: room identifier cannot be empty")
	}
	parts := strings.SplitN(body, ":", 2)
	if parts[0] == "" || !isValidRoomLocalpart(parts[0]) {
		return errors.New("matrix_room_id format is invalid: identifier contains invalid characters")
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return errors.New("matrix_room_id format is invalid: server name cannot be empty")
		}
		if err := validateServerName(parts[1]); err != nil {
			return fmt.Errorf("matrix_room_id server is invalid: %w", err)
		}
	}
	return nil
}

func validateURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL structure: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New("URL host is missing")
	}
	return validateServerName(parsed.Host)
}

func validateSMTP(smtp *SMTPConfig) error {
	if smtp.Host == "" {
		return errors.New("smtp_host must not be empty")
	}
	if smtp.Port != 465 && smtp.Port != 587 && smtp.Port != 25 {
		return fmt.Errorf("unsupported smtp_port %d: expected 465, 587, or 25", smtp.Port)
	}
	if _, err := mail.ParseAddress(smtp.Mail); err != nil {
		return fmt.Errorf("invalid smtp_mail address: %w", err)
	}
	if smtp.Password == "" {
		return errors.New("smtp_password must not be empty")
	}
	return nil
}

func validateTelegram(tg TelegramConfig) error {
	if !tg.Enabled {
		return nil
	}
	if !telegramTokenRegexp.MatchString(tg.Token) {
		return errors.New("telegram_bot_token format is invalid")
	}
	if _, err := strconv.ParseInt(tg.ChatID, 10, 64); err != nil {
		return fmt.Errorf("telegram_chat_id must be a valid integer: %w", err)
	}
	return nil
}

func validateMatrix(mx MatrixConfig) error {
	if !mx.Enabled {
		return nil
	}
	if err := validateURL(mx.URL); err != nil {
		return fmt.Errorf("matrix_url invalid: %w", err)
	}
	if err := validateRoomID(mx.RoomID); err != nil {
		return err
	}
	if mx.AccessToken == "" {
		return errors.New("matrix_access_token must not be empty when matrix is configured")
	}
	return nil
}

func validateNtfy(nt NtfyConfig) error {
	if !nt.Enabled {
		return nil
	}
	if err := validateURL(nt.URL); err != nil {
		return fmt.Errorf("ntfy_url invalid: %w", err)
	}
	if !ntfyTopicRegexp.MatchString(nt.Topic) {
		return errors.New("ntfy_topic format is invalid: must contain alphanumeric, underscores, or dashes")
	}
	return nil
}

func parseBoolEnv(key string, fallback bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return fallback
	}
	return parsed
}

func validateChannel(ch string, smtpEnabled, tgEnabled, mxEnabled, ntEnabled bool) error {
	switch ch {
	case "smtp":
		if !smtpEnabled {
			return ErrSMTPDisabledExplicit
		}
	case "telegram":
		if !tgEnabled {
			return ErrTelegramNotConfigured
		}
	case "matrix":
		if !mxEnabled {
			return ErrMatrixNotConfigured
		}
	case "ntfy":
		if !ntEnabled {
			return ErrNtfyNotConfigured
		}
	default:
		return fmt.Errorf("unknown notification channel %q in configuration", ch)
	}
	return nil
}

func loadSMTP(dir string, enabled bool) (SMTPConfig, error) {
	if !enabled {
		return SMTPConfig{Enabled: false}, nil
	}
	host, err := readSecretFile(dir, "smtp_host")
	if err != nil {
		return SMTPConfig{}, err
	}
	portRaw, err := readSecretFile(dir, "smtp_port")
	if err != nil {
		return SMTPConfig{}, err
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		return SMTPConfig{}, fmt.Errorf("invalid smtp_port numeric value: %w", err)
	}
	mailAddr, err := readSecretFile(dir, "smtp_mail")
	if err != nil {
		return SMTPConfig{}, err
	}
	pass, err := readSecretFile(dir, "smtp_password")
	if err != nil {
		return SMTPConfig{}, err
	}

	cfg := SMTPConfig{
		Host:     host,
		Port:     port,
		Mail:     mailAddr,
		Password: pass,
		FromName: os.Getenv("SMTP_FROM_NAME"),
		Enabled:  true,
	}
	if err = validateSMTP(&cfg); err != nil {
		return SMTPConfig{}, fmt.Errorf("validate smtp config: %w", err)
	}
	return cfg, nil
}

func loadTelegram(dir string) (TelegramConfig, error) {
	token, err := readOptionalSecretFile(dir, "telegram_token")
	if err != nil {
		return TelegramConfig{}, err
	}
	if token == "" {
		token, err = readOptionalSecretFile(dir, "telegram_bot_token")
		if err != nil {
			return TelegramConfig{}, err
		}
	}
	chatID, err := readOptionalSecretFile(dir, "telegram_chat_id")
	if err != nil {
		return TelegramConfig{}, err
	}
	cfg := TelegramConfig{
		Enabled: token != "" && chatID != "",
		Token:   token,
		ChatID:  chatID,
	}
	if err = validateTelegram(cfg); err != nil {
		return TelegramConfig{}, fmt.Errorf("validate telegram config: %w", err)
	}
	return cfg, nil
}

func loadMatrix(dir string) (MatrixConfig, error) {
	u, err := readOptionalSecretFile(dir, "matrix_url")
	if err != nil {
		return MatrixConfig{}, err
	}
	room, err := readOptionalSecretFile(dir, "matrix_room_id")
	if err != nil {
		return MatrixConfig{}, err
	}
	token, err := readOptionalSecretFile(dir, "matrix_access_token")
	if err != nil {
		return MatrixConfig{}, err
	}
	cfg := MatrixConfig{
		Enabled:     u != "" && room != "" && token != "",
		URL:         u,
		RoomID:      room,
		AccessToken: token,
	}
	if err = validateMatrix(cfg); err != nil {
		return MatrixConfig{}, fmt.Errorf("validate matrix config: %w", err)
	}
	return cfg, nil
}

func loadNtfy(dir string) (NtfyConfig, error) {
	u, err := readOptionalSecretFile(dir, "ntfy_url")
	if err != nil {
		return NtfyConfig{}, err
	}
	topic, err := readOptionalSecretFile(dir, "ntfy_topic")
	if err != nil {
		return NtfyConfig{}, err
	}
	token, err := readOptionalSecretFile(dir, "ntfy_token")
	if err != nil {
		return NtfyConfig{}, err
	}
	cfg := NtfyConfig{
		Enabled: u != "" && topic != "",
		URL:     u,
		Topic:   topic,
		Token:   token,
	}
	if err = validateNtfy(cfg); err != nil {
		return NtfyConfig{}, fmt.Errorf("validate ntfy config: %w", err)
	}
	return cfg, nil
}

func resolveSiteName() string {
	if val := os.Getenv("SITE_NAME"); val != "" {
		return val
	}
	if val := os.Getenv("SITE_USER"); val != "" {
		return val
	}
	if val := os.Getenv("SERVER_NAME"); val != "" {
		return val
	}
	return "WordPress Site"
}

func resolveAdminEmails(dir string) ([]string, error) {
	adminEmailsRaw, err := readOptionalSecretFile(dir, "admin_emails")
	if err != nil {
		return nil, err
	}
	adminEmails := parseEmails(adminEmailsRaw)
	if len(adminEmails) == 0 {
		adminEmails = parseEmails(os.Getenv("NOTIFY_ADMIN_EMAILS"))
	}
	return adminEmails, nil
}

// LoadConfig loads and validates mandatory secrets, optional credentials, and environment overrides.
func LoadConfig(mandatorySecretsDir, optionalSecretsDir string) (*Config, error) {
	smtpEnabled := parseBoolEnv("NOTIFY_SMTP_ENABLED", true)
	adminFilterRequired := parseBoolEnv("NOTIFY_ADMIN_FILTER_REQUIRED", true)

	smtpCfg, err := loadSMTP(mandatorySecretsDir, smtpEnabled)
	if err != nil {
		return nil, err
	}
	tgCfg, err := loadTelegram(optionalSecretsDir)
	if err != nil {
		return nil, err
	}
	mxCfg, err := loadMatrix(optionalSecretsDir)
	if err != nil {
		return nil, err
	}
	ntCfg, err := loadNtfy(optionalSecretsDir)
	if err != nil {
		return nil, err
	}

	if !smtpCfg.Enabled && !tgCfg.Enabled && !mxCfg.Enabled && !ntCfg.Enabled {
		return nil, ErrZeroChannelsConfigured
	}

	defaultChannels, err := parseChannels(os.Getenv("NOTIFY_CHANNELS"), smtpCfg.Enabled, tgCfg.Enabled, mxCfg.Enabled, ntCfg.Enabled)
	if err != nil {
		return nil, err
	}

	routes, err := parseRoutes(smtpCfg.Enabled, tgCfg.Enabled, mxCfg.Enabled, ntCfg.Enabled)
	if err != nil {
		return nil, err
	}

	adminEmails, err := resolveAdminEmails(optionalSecretsDir)
	if err != nil {
		return nil, err
	}

	socketPath := os.Getenv("NOTIFY_SOCKET_PATH")
	if socketPath == "" {
		socketPath = defaultSocketPath
	}

	return &Config{
		SocketPath:          socketPath,
		SiteName:            resolveSiteName(),
		Ntfy:                ntCfg,
		Matrix:              mxCfg,
		Telegram:            tgCfg,
		Routes:              routes,
		DefaultChannels:     defaultChannels,
		AdminEmails:         adminEmails,
		SMTP:                smtpCfg,
		MaxAttachmentSizeMB: parsePositiveInt(os.Getenv("NOTIFY_MAX_ATTACHMENT_SIZE_MB"), defaultMaxAttachmentMB),
		RateLimitPerMinute:  parsePositiveInt(os.Getenv("NOTIFY_RATE_LIMIT_PER_MINUTE"), defaultRateLimitPerMinute),
		BurstLimit:          parsePositiveInt(os.Getenv("NOTIFY_BURST"), defaultBurstLimit),
		SMTPEnabled:         smtpEnabled,
		AdminFilterRequired: adminFilterRequired,
	}, nil
}

func parseChannels(raw string, smtpEnabled, tgEnabled, mxEnabled, ntEnabled bool) ([]string, error) {
	if raw != "" {
		parts := strings.Split(raw, ",")
		res := make([]string, 0, len(parts))
		for _, p := range parts {
			trimmed := strings.ToLower(strings.TrimSpace(p))
			if trimmed != "" {
				if err := validateChannel(trimmed, smtpEnabled, tgEnabled, mxEnabled, ntEnabled); err != nil {
					return nil, err
				}
				res = append(res, trimmed)
			}
		}
		if len(res) > 0 {
			return res, nil
		}
	}

	channels := make([]string, 0, 4)
	if smtpEnabled {
		channels = append(channels, "smtp")
	}
	if tgEnabled {
		channels = append(channels, "telegram")
	}
	if mxEnabled {
		channels = append(channels, "matrix")
	}
	if ntEnabled {
		channels = append(channels, "ntfy")
	}
	return channels, nil
}

func parseRouteTargets(raw string, smtpEnabled, tgEnabled, mxEnabled, ntEnabled bool) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	rawList := strings.Split(raw, ",")
	targets := make([]string, 0, len(rawList))
	for _, t := range rawList {
		clean := strings.ToLower(strings.TrimSpace(t))
		if clean != "" {
			if err := validateChannel(clean, smtpEnabled, tgEnabled, mxEnabled, ntEnabled); err != nil {
				return nil, err
			}
			targets = append(targets, clean)
		}
	}
	return targets, nil
}

func parseSingleRoute(key, matchPattern string, smtpEnabled, tgEnabled, mxEnabled, ntEnabled bool) (*RouteRule, error) {
	re, err := regexp.Compile(matchPattern)
	if err != nil {
		return nil, fmt.Errorf("compile route regex %q: %w", matchPattern, err)
	}

	prefix := strings.TrimSuffix(key, "_MATCH")
	targets, err := parseRouteTargets(os.Getenv(prefix+"_TARGETS"), smtpEnabled, tgEnabled, mxEnabled, ntEnabled)
	if err != nil {
		return nil, err
	}

	return &RouteRule{
		Name:            strings.TrimPrefix(prefix, "NOTIFY_RULE_"),
		MatchRegex:      re,
		Targets:         targets,
		SendAttachments: strings.EqualFold(os.Getenv(prefix+"_ATTACHMENTS"), "true"),
	}, nil
}

func parseRoutes(smtpEnabled, tgEnabled, mxEnabled, ntEnabled bool) ([]RouteRule, error) {
	var routes []RouteRule
	for _, env := range osEnviron() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, "NOTIFY_RULE_") || !strings.HasSuffix(key, "_MATCH") {
			continue
		}
		rule, err := parseSingleRoute(key, parts[1], smtpEnabled, tgEnabled, mxEnabled, ntEnabled)
		if err != nil {
			return nil, err
		}
		if rule != nil {
			routes = append(routes, *rule)
		}
	}
	return routes, nil
}
