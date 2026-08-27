package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	mailmessage "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/google/uuid"
)

type GroupApplicationSMTPConfig struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
	FromAddress        string `json:"from_address"`
	FromName           string `json:"from_name"`
	TLSMode            string `json:"tls_mode"`
}

type GroupApplicationIMAPConfig struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"password,omitempty"`
	PasswordConfigured  bool   `json:"password_configured"`
	UseSMTPCredentials  bool   `json:"use_smtp_credentials"`
	Mailbox             string `json:"mailbox"`
	ReplyAddress        string `json:"reply_address"`
	TLSMode             string `json:"tls_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type GroupApplicationEmailConfig struct {
	Enabled        bool                       `json:"enabled"`
	SMTP           GroupApplicationSMTPConfig `json:"smtp"`
	IMAP           GroupApplicationIMAPConfig `json:"imap"`
	LegacyImported bool                       `json:"legacy_imported,omitempty"`
}

const (
	groupApplicationIMAPDialTimeout = 8 * time.Second
	groupApplicationIMAPTestTimeout = 10 * time.Second
	groupApplicationIMAPPollTimeout = 60 * time.Second
)

type storedGroupApplicationSMTPConfig struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Username          string `json:"username"`
	EncryptedPassword string `json:"encrypted_password"`
	FromAddress       string `json:"from_address"`
	FromName          string `json:"from_name"`
	TLSMode           string `json:"tls_mode"`
}

type storedGroupApplicationIMAPConfig struct {
	Enabled             bool   `json:"enabled,omitempty"` // legacy field
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	EncryptedPassword   string `json:"encrypted_password"`
	UseSMTPCredentials  bool   `json:"use_smtp_credentials"`
	Mailbox             string `json:"mailbox"`
	ReplyAddress        string `json:"reply_address"`
	TLSMode             string `json:"tls_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type storedGroupApplicationEmailConfig struct {
	Enabled bool                             `json:"enabled"`
	SMTP    storedGroupApplicationSMTPConfig `json:"smtp"`
	IMAP    storedGroupApplicationIMAPConfig `json:"imap"`
}

func defaultGroupApplicationEmailConfig() GroupApplicationEmailConfig {
	return GroupApplicationEmailConfig{
		SMTP: GroupApplicationSMTPConfig{Port: 587, TLSMode: "starttls"},
		IMAP: GroupApplicationIMAPConfig{Port: 993, Mailbox: "INBOX", TLSMode: "implicit", PollIntervalSeconds: 60},
	}
}

func defaultStoredGroupApplicationEmailConfig() storedGroupApplicationEmailConfig {
	defaults := defaultGroupApplicationEmailConfig()
	return storedGroupApplicationEmailConfig{
		SMTP: storedGroupApplicationSMTPConfig{Port: defaults.SMTP.Port, TLSMode: defaults.SMTP.TLSMode},
		IMAP: storedGroupApplicationIMAPConfig{Port: defaults.IMAP.Port, Mailbox: defaults.IMAP.Mailbox, TLSMode: defaults.IMAP.TLSMode, PollIntervalSeconds: defaults.IMAP.PollIntervalSeconds},
	}
}

func normalizeGroupApplicationEmailConfig(input GroupApplicationEmailConfig) GroupApplicationEmailConfig {
	defaults := defaultGroupApplicationEmailConfig()
	input.SMTP.Host = strings.TrimSpace(input.SMTP.Host)
	input.SMTP.Username = strings.TrimSpace(input.SMTP.Username)
	input.SMTP.FromAddress = strings.TrimSpace(input.SMTP.FromAddress)
	input.SMTP.FromName = strings.TrimSpace(input.SMTP.FromName)
	input.SMTP.TLSMode = strings.ToLower(strings.TrimSpace(input.SMTP.TLSMode))
	if input.SMTP.Port == 0 {
		input.SMTP.Port = defaults.SMTP.Port
	}
	if input.SMTP.TLSMode == "" {
		input.SMTP.TLSMode = defaults.SMTP.TLSMode
	}
	input.IMAP.Host = strings.TrimSpace(input.IMAP.Host)
	input.IMAP.Username = strings.TrimSpace(input.IMAP.Username)
	input.IMAP.Mailbox = strings.TrimSpace(input.IMAP.Mailbox)
	input.IMAP.ReplyAddress = strings.TrimSpace(input.IMAP.ReplyAddress)
	input.IMAP.TLSMode = strings.ToLower(strings.TrimSpace(input.IMAP.TLSMode))
	if input.IMAP.Port == 0 {
		input.IMAP.Port = defaults.IMAP.Port
	}
	if input.IMAP.Mailbox == "" {
		input.IMAP.Mailbox = defaults.IMAP.Mailbox
	}
	if input.IMAP.TLSMode == "" {
		input.IMAP.TLSMode = defaults.IMAP.TLSMode
	}
	if input.IMAP.PollIntervalSeconds == 0 {
		input.IMAP.PollIntervalSeconds = defaults.IMAP.PollIntervalSeconds
	}
	return input
}

func applyStoredGroupApplicationEmailDefaults(stored *storedGroupApplicationEmailConfig) {
	defaults := defaultStoredGroupApplicationEmailConfig()
	if stored.SMTP.Port == 0 {
		stored.SMTP.Port = defaults.SMTP.Port
	}
	if stored.SMTP.TLSMode == "" {
		stored.SMTP.TLSMode = defaults.SMTP.TLSMode
	}
	if stored.IMAP.Port == 0 {
		stored.IMAP.Port = defaults.IMAP.Port
	}
	if stored.IMAP.Mailbox == "" {
		stored.IMAP.Mailbox = defaults.IMAP.Mailbox
	}
	if stored.IMAP.TLSMode == "" {
		stored.IMAP.TLSMode = defaults.IMAP.TLSMode
	}
	if stored.IMAP.PollIntervalSeconds == 0 {
		stored.IMAP.PollIntervalSeconds = defaults.IMAP.PollIntervalSeconds
	}
}

func (s *GroupApplicationService) loadStoredEmailConfig(ctx context.Context) (storedGroupApplicationEmailConfig, bool, error) {
	stored := defaultStoredGroupApplicationEmailConfig()
	if s.settingRepo == nil {
		return stored, false, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGroupApplicationEmail)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return storedGroupApplicationEmailConfig{}, false, fmt.Errorf("read group application email settings: %w", err)
	}
	if err == nil && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return storedGroupApplicationEmailConfig{}, false, fmt.Errorf("decode group application email settings: %w", err)
		}
		applyStoredGroupApplicationEmailDefaults(&stored)
		return stored, false, nil
	}

	// The pre-module IMAP setting is imported only when the new setting does not
	// exist. It never enables the workflow and never supplies SMTP credentials.
	raw, err = s.settingRepo.GetValue(ctx, SettingKeyGroupApplicationIMAP)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return storedGroupApplicationEmailConfig{}, false, fmt.Errorf("read legacy group application IMAP settings: %w", err)
	}
	if err != nil || strings.TrimSpace(raw) == "" {
		return stored, false, nil
	}
	var legacy storedGroupApplicationIMAPConfig
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return storedGroupApplicationEmailConfig{}, false, fmt.Errorf("decode legacy group application IMAP settings: %w", err)
	}
	stored.IMAP = legacy
	stored.IMAP.Enabled = false
	stored.Enabled = false
	applyStoredGroupApplicationEmailDefaults(&stored)
	return stored, true, nil
}

func publicGroupApplicationEmailConfig(stored storedGroupApplicationEmailConfig, legacy bool) *GroupApplicationEmailConfig {
	return &GroupApplicationEmailConfig{
		Enabled: stored.Enabled,
		SMTP: GroupApplicationSMTPConfig{
			Host: stored.SMTP.Host, Port: stored.SMTP.Port, Username: stored.SMTP.Username,
			PasswordConfigured: stored.SMTP.EncryptedPassword != "", FromAddress: stored.SMTP.FromAddress,
			FromName: stored.SMTP.FromName, TLSMode: stored.SMTP.TLSMode,
		},
		IMAP: GroupApplicationIMAPConfig{
			Host: stored.IMAP.Host, Port: stored.IMAP.Port, Username: stored.IMAP.Username,
			PasswordConfigured: stored.IMAP.EncryptedPassword != "" || stored.IMAP.UseSMTPCredentials && stored.SMTP.EncryptedPassword != "",
			UseSMTPCredentials: stored.IMAP.UseSMTPCredentials, Mailbox: stored.IMAP.Mailbox,
			ReplyAddress: stored.IMAP.ReplyAddress, TLSMode: stored.IMAP.TLSMode,
			PollIntervalSeconds: stored.IMAP.PollIntervalSeconds,
		},
		LegacyImported: legacy,
	}
}

func (s *GroupApplicationService) GetEmailConfig(ctx context.Context) (*GroupApplicationEmailConfig, error) {
	stored, legacy, err := s.loadStoredEmailConfig(ctx)
	if err != nil {
		return nil, err
	}
	return publicGroupApplicationEmailConfig(stored, legacy), nil
}

func sameGroupApplicationCredentialIdentity(oldHost, oldUsername, host, username string) bool {
	return strings.EqualFold(strings.TrimSpace(oldHost), strings.TrimSpace(host)) && strings.EqualFold(strings.TrimSpace(oldUsername), strings.TrimSpace(username))
}

func (s *GroupApplicationService) resolveGroupApplicationPassword(label, password, existingCiphertext string, identityUnchanged bool) (string, error) {
	if password == "********" {
		password = ""
	}
	if password == "" {
		if existingCiphertext != "" && !identityUnchanged {
			return "", fmt.Errorf("%s host or username changed; enter the password again", label)
		}
		return existingCiphertext, nil
	}
	if s.encryptor == nil {
		return "", errors.New("group application email encryption is unavailable")
	}
	encrypted, err := s.encryptor.Encrypt(password)
	if err != nil {
		return "", fmt.Errorf("encrypt %s password: %w", label, err)
	}
	return encrypted, nil
}

func (s *GroupApplicationService) buildStoredEmailConfig(ctx context.Context, input GroupApplicationEmailConfig, passwordScope string) (storedGroupApplicationEmailConfig, error) {
	input = normalizeGroupApplicationEmailConfig(input)
	existing, _, err := s.loadStoredEmailConfig(ctx)
	if err != nil {
		return storedGroupApplicationEmailConfig{}, err
	}
	stored := storedGroupApplicationEmailConfig{
		Enabled: input.Enabled,
		SMTP: storedGroupApplicationSMTPConfig{
			Host: input.SMTP.Host, Port: input.SMTP.Port, Username: input.SMTP.Username,
			FromAddress: input.SMTP.FromAddress, FromName: input.SMTP.FromName, TLSMode: input.SMTP.TLSMode,
		},
		IMAP: storedGroupApplicationIMAPConfig{
			Host: input.IMAP.Host, Port: input.IMAP.Port, Username: input.IMAP.Username,
			UseSMTPCredentials: input.IMAP.UseSMTPCredentials, Mailbox: input.IMAP.Mailbox,
			ReplyAddress: input.IMAP.ReplyAddress, TLSMode: input.IMAP.TLSMode,
			PollIntervalSeconds: input.IMAP.PollIntervalSeconds,
		},
	}
	resolveSMTP := passwordScope == "" || passwordScope == "smtp" || passwordScope == "imap" && stored.IMAP.UseSMTPCredentials
	if resolveSMTP {
		stored.SMTP.EncryptedPassword, err = s.resolveGroupApplicationPassword(
			"SMTP", input.SMTP.Password, existing.SMTP.EncryptedPassword,
			sameGroupApplicationCredentialIdentity(existing.SMTP.Host, existing.SMTP.Username, stored.SMTP.Host, stored.SMTP.Username),
		)
		if err != nil {
			return storedGroupApplicationEmailConfig{}, err
		}
	}
	if stored.IMAP.UseSMTPCredentials {
		stored.IMAP.Username = ""
		stored.IMAP.EncryptedPassword = ""
	} else if passwordScope == "" || passwordScope == "imap" {
		stored.IMAP.EncryptedPassword, err = s.resolveGroupApplicationPassword(
			"IMAP", input.IMAP.Password, existing.IMAP.EncryptedPassword,
			sameGroupApplicationCredentialIdentity(existing.IMAP.Host, existing.IMAP.Username, stored.IMAP.Host, stored.IMAP.Username),
		)
		if err != nil {
			return storedGroupApplicationEmailConfig{}, err
		}
	}
	return stored, nil
}

func (s *GroupApplicationService) SaveEmailConfig(ctx context.Context, input GroupApplicationEmailConfig) (*GroupApplicationEmailConfig, error) {
	if s.settingRepo == nil || s.encryptor == nil {
		return nil, errors.New("group application email settings are unavailable")
	}
	stored, err := s.buildStoredEmailConfig(ctx, input, "")
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	if err := validateStoredGroupApplicationEmailConfig(stored, stored.Enabled); err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGroupApplicationEmail, string(raw)); err != nil {
		return nil, err
	}
	return publicGroupApplicationEmailConfig(stored, false), nil
}

func validateGroupApplicationTLSMode(label, mode string) error {
	if mode != "implicit" && mode != "starttls" {
		return fmt.Errorf("%s TLS mode must be implicit or starttls", label)
	}
	return nil
}

func validateStoredGroupApplicationSMTPConfig(config storedGroupApplicationSMTPConfig, required bool) error {
	if !required && config.Host == "" && config.Username == "" && config.EncryptedPassword == "" && config.FromAddress == "" {
		return nil
	}
	if config.Host == "" || config.Username == "" || config.EncryptedPassword == "" || config.FromAddress == "" {
		return errors.New("SMTP host, username, password and sender address are required")
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("SMTP port must be between 1 and 65535")
	}
	if err := validateGroupApplicationTLSMode("SMTP", config.TLSMode); err != nil {
		return err
	}
	if _, err := NormalizeGroupApplicationEmail(config.FromAddress); err != nil {
		return errors.New("invalid SMTP sender address")
	}
	return nil
}

func validateStoredGroupApplicationIMAPConfig(config storedGroupApplicationIMAPConfig, smtpConfig storedGroupApplicationSMTPConfig, required bool) error {
	username, password := config.Username, config.EncryptedPassword
	if config.UseSMTPCredentials {
		username, password = smtpConfig.Username, smtpConfig.EncryptedPassword
	}
	if !required && config.Host == "" && username == "" && password == "" && config.ReplyAddress == "" {
		return nil
	}
	if config.Host == "" || username == "" || password == "" || config.ReplyAddress == "" || config.Mailbox == "" {
		return errors.New("IMAP host, username, password, mailbox and reply address are required")
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("IMAP port must be between 1 and 65535")
	}
	if err := validateGroupApplicationTLSMode("IMAP", config.TLSMode); err != nil {
		return err
	}
	if config.PollIntervalSeconds < 15 || config.PollIntervalSeconds > 300 {
		return errors.New("IMAP poll interval must be between 15 and 300 seconds")
	}
	if _, err := NormalizeGroupApplicationEmail(config.ReplyAddress); err != nil {
		return errors.New("invalid IMAP reply address")
	}
	return nil
}

func validateStoredGroupApplicationEmailConfig(config storedGroupApplicationEmailConfig, requireEnabled bool) error {
	if requireEnabled && !config.Enabled {
		return errors.New("group application workflow is disabled")
	}
	if !config.Enabled && !requireEnabled {
		return nil
	}
	if err := validateStoredGroupApplicationSMTPConfig(config.SMTP, true); err != nil {
		return err
	}
	return validateStoredGroupApplicationIMAPConfig(config.IMAP, config.SMTP, true)
}

func (s *GroupApplicationService) runtimeEmailConfig(stored storedGroupApplicationEmailConfig, requireEnabled bool) (*GroupApplicationEmailConfig, error) {
	if err := validateStoredGroupApplicationEmailConfig(stored, requireEnabled); err != nil {
		return nil, err
	}
	public := publicGroupApplicationEmailConfig(stored, false)
	if s.encryptor == nil {
		if stored.SMTP.EncryptedPassword != "" || stored.IMAP.EncryptedPassword != "" {
			return nil, errors.New("group application email encryption is unavailable")
		}
		return public, nil
	}
	var err error
	if stored.SMTP.EncryptedPassword != "" {
		public.SMTP.Password, err = s.encryptor.Decrypt(stored.SMTP.EncryptedPassword)
		if err != nil {
			return nil, fmt.Errorf("decrypt SMTP password: %w", err)
		}
	}
	if stored.IMAP.UseSMTPCredentials {
		public.IMAP.Username = public.SMTP.Username
		public.IMAP.Password = public.SMTP.Password
	} else if stored.IMAP.EncryptedPassword != "" {
		public.IMAP.Password, err = s.encryptor.Decrypt(stored.IMAP.EncryptedPassword)
		if err != nil {
			return nil, fmt.Errorf("decrypt IMAP password: %w", err)
		}
	}
	return public, nil
}

func (s *GroupApplicationService) LoadEmailConfig(ctx context.Context, requireEnabled bool) (*GroupApplicationEmailConfig, error) {
	stored, _, err := s.loadStoredEmailConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s.runtimeEmailConfig(stored, requireEnabled)
}

func (s *GroupApplicationService) ResolveEmailConfigForTest(ctx context.Context, input GroupApplicationEmailConfig, transport string) (*GroupApplicationEmailConfig, error) {
	stored, err := s.buildStoredEmailConfig(ctx, input, transport)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	switch transport {
	case "smtp":
		err = validateStoredGroupApplicationSMTPConfig(stored.SMTP, true)
	case "imap":
		err = validateStoredGroupApplicationIMAPConfig(stored.IMAP, stored.SMTP, true)
	default:
		err = errors.New("unsupported group application email transport")
	}
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	stored.Enabled = false
	return s.runtimeEmailConfig(stored, false)
}

func (config *GroupApplicationEmailConfig) SMTPTransportConfig() *SMTPConfig {
	if config == nil {
		return nil
	}
	return &SMTPConfig{
		Host: config.SMTP.Host, Port: config.SMTP.Port, Username: config.SMTP.Username,
		Password: config.SMTP.Password, From: config.SMTP.FromAddress, FromName: config.SMTP.FromName,
		TLSMode: config.SMTP.TLSMode,
	}
}

type GroupApplicationWorkerHealth struct {
	Running            bool      `json:"running"`
	WorkflowEnabled    bool      `json:"workflow_enabled"`
	MailProcessed      uint64    `json:"mail_processed"`
	MailFailures       uint64    `json:"mail_failures"`
	RepliesProcessed   uint64    `json:"replies_processed"`
	ReplyFailures      uint64    `json:"reply_failures"`
	LastMailCheckAt    time.Time `json:"last_mail_check_at,omitempty"`
	LastMailError      string    `json:"last_mail_error,omitempty"`
	LastIMAPCheckAt    time.Time `json:"last_imap_check_at,omitempty"`
	LastIMAPError      string    `json:"last_imap_error,omitempty"`
	ConfigurationError string    `json:"configuration_error,omitempty"`
}

type groupApplicationEmailSender interface {
	SendEmailWithConfigAndOptions(config *SMTPConfig, to, subject, body string, options EmailSendOptions) error
	TestSMTPConnectionWithConfig(config *SMTPConfig) error
}

type GroupApplicationWorker struct {
	repo             GroupApplicationRepository
	service          *GroupApplicationService
	email            groupApplicationEmailSender
	workerID         string
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	start            sync.Once
	stop             sync.Once
	running          atomic.Bool
	mailProcessed    atomic.Uint64
	mailFailures     atomic.Uint64
	repliesProcessed atomic.Uint64
	replyFailures    atomic.Uint64
	lastIMAPCheck    atomic.Value // time.Time
	lastIMAPError    atomic.Value // string
	lastMailCheck    atomic.Value // time.Time
	lastMailError    atomic.Value // string
	configError      atomic.Value // string
	workflowEnabled  atomic.Bool
	imapPoller       func(context.Context, *GroupApplicationIMAPConfig) error
}

func NewGroupApplicationWorker(repo GroupApplicationRepository, service *GroupApplicationService, email *EmailService) *GroupApplicationWorker {
	return newGroupApplicationWorker(repo, service, email)
}

func newGroupApplicationWorker(repo GroupApplicationRepository, service *GroupApplicationService, email groupApplicationEmailSender) *GroupApplicationWorker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &GroupApplicationWorker{repo: repo, service: service, email: email, workerID: uuid.NewString(), ctx: ctx, cancel: cancel}
	w.lastIMAPCheck.Store(time.Time{})
	w.lastIMAPError.Store("")
	w.lastMailCheck.Store(time.Time{})
	w.lastMailError.Store("")
	w.configError.Store("")
	return w
}

func ProvideGroupApplicationWorker(repo GroupApplicationRepository, service *GroupApplicationService, email *EmailService) *GroupApplicationWorker {
	w := NewGroupApplicationWorker(repo, service, email)
	w.Start()
	return w
}

func (w *GroupApplicationWorker) Start() {
	if w == nil || w.repo == nil || w.service == nil || w.email == nil {
		return
	}
	w.start.Do(func() { w.running.Store(true); w.wg.Add(1); go w.run() })
}

func (w *GroupApplicationWorker) Stop() {
	if w == nil {
		return
	}
	w.stop.Do(func() { w.cancel(); w.wg.Wait(); w.running.Store(false) })
}

func (w *GroupApplicationWorker) Health() GroupApplicationWorkerHealth {
	if w == nil {
		return GroupApplicationWorkerHealth{}
	}
	health := GroupApplicationWorkerHealth{Running: w.running.Load(), WorkflowEnabled: w.workflowEnabled.Load(), MailProcessed: w.mailProcessed.Load(), MailFailures: w.mailFailures.Load(), RepliesProcessed: w.repliesProcessed.Load(), ReplyFailures: w.replyFailures.Load()}
	if value, ok := w.lastIMAPCheck.Load().(time.Time); ok {
		health.LastIMAPCheckAt = value
	}
	if value, ok := w.lastIMAPError.Load().(string); ok {
		health.LastIMAPError = value
	}
	if value, ok := w.lastMailCheck.Load().(time.Time); ok {
		health.LastMailCheckAt = value
	}
	if value, ok := w.lastMailError.Load().(string); ok {
		health.LastMailError = value
	}
	if value, ok := w.configError.Load().(string); ok {
		health.ConfigurationError = value
	}
	return health
}

func (w *GroupApplicationWorker) RefreshConfiguration(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	_, _ = w.refreshWorkflowState(ctx)
}

func (w *GroupApplicationWorker) refreshWorkflowState(ctx context.Context) (*GroupApplicationEmailConfig, error) {
	cfg, err := w.service.LoadEmailConfig(ctx, false)
	w.workflowEnabled.Store(err == nil && cfg != nil && cfg.Enabled)
	if err != nil {
		w.configError.Store(boundedGroupApplicationError(err))
	} else {
		w.configError.Store("")
	}
	return cfg, err
}

func (w *GroupApplicationWorker) run() {
	defer w.wg.Done()
	defer w.running.Store(false)
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		w.runMailLoop()
	}()
	go func() {
		defer loops.Done()
		w.runIMAPLoop()
	}()
	loops.Wait()
}

func (w *GroupApplicationWorker) runMailLoop() {
	mailTicker := time.NewTicker(time.Second)
	defer mailTicker.Stop()
	for {
		w.lastMailCheck.Store(time.Now().UTC())
		if err := w.processMailBatch(w.ctx); err != nil && w.ctx.Err() == nil {
			w.mailFailures.Add(1)
			w.lastMailError.Store(boundedGroupApplicationError(err))
			slog.Warn("group application mail outbox failed", "error", err)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-mailTicker.C:
		}
	}
}

func (w *GroupApplicationWorker) runIMAPLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextPoll := time.Now()
	for {
		if !time.Now().Before(nextPoll) {
			interval := 60 * time.Second
			cfg, configErr := w.refreshWorkflowState(w.ctx)
			if configErr == nil && cfg != nil && cfg.Enabled {
				interval = time.Duration(cfg.IMAP.PollIntervalSeconds) * time.Second
				w.lastIMAPCheck.Store(time.Now().UTC())
				pollCtx, cancel := context.WithTimeout(w.ctx, groupApplicationIMAPPollTimeout)
				err := w.pollIMAPOnce(pollCtx, &cfg.IMAP)
				cancel()
				if err != nil && w.ctx.Err() == nil {
					w.replyFailures.Add(1)
					w.lastIMAPError.Store(boundedGroupApplicationError(err))
					slog.Warn("group application IMAP poll failed", "error", err)
				} else if err == nil {
					w.lastIMAPError.Store("")
				}
			} else if configErr == nil {
				w.lastIMAPError.Store("")
			}
			nextPoll = time.Now().Add(interval)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *GroupApplicationWorker) pollIMAPOnce(ctx context.Context, cfg *GroupApplicationIMAPConfig) error {
	if w.imapPoller != nil {
		return w.imapPoller(ctx, cfg)
	}
	return w.pollIMAP(ctx, cfg)
}

func (w *GroupApplicationWorker) processMailBatch(ctx context.Context) error {
	cfg, err := w.service.LoadEmailConfig(ctx, false)
	if err != nil {
		return err
	}
	w.lastMailError.Store("")
	if !cfg.Enabled {
		return nil
	}
	jobs, err := w.repo.ClaimMail(ctx, w.workerID, 20, time.Minute)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		options := EmailSendOptions{MessageID: job.MessageID}
		if job.Attachment != nil {
			options.Attachment = &EmailAttachment{Filename: job.Attachment.Filename, ContentType: job.Attachment.ContentType, Data: job.Attachment.Data}
		}
		var sendErr error
		if job.Kind == GroupApplicationMailApproval {
			options.ReplyTo = cfg.IMAP.ReplyAddress
		}
		if sendErr == nil {
			if w.email == nil {
				sendErr = errors.New("group application email sender is unavailable")
			} else if err := ctx.Err(); err != nil {
				sendErr = err
			} else {
				sendErr = w.email.SendEmailWithConfigAndOptions(cfg.SMTPTransportConfig(), job.Recipient, job.Subject, job.HTMLBody, options)
			}
		}
		if sendErr == nil {
			if ackErr := w.repo.MarkMailSent(ctx, job.ID, w.workerID); ackErr != nil {
				return ackErr
			}
			w.mailProcessed.Add(1)
			w.lastMailError.Store("")
			continue
		}
		w.mailFailures.Add(1)
		w.lastMailError.Store(boundedGroupApplicationError(sendErr))
		attempt := job.Attempts + 1
		terminal := attempt >= 8
		if retryErr := w.repo.RetryClaimedMail(ctx, job.ID, w.workerID, time.Now().Add(groupApplicationRetryDelay(attempt)), terminal, boundedGroupApplicationError(sendErr)); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func groupApplicationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	base := time.Second * time.Duration(1<<(attempt-1))
	if base > 15*time.Minute {
		base = 15 * time.Minute
	}
	return time.Duration(float64(base) * (0.8 + rand.Float64()*0.4))
}
func boundedGroupApplicationError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

func (w *GroupApplicationWorker) TestSMTP(ctx context.Context, input GroupApplicationEmailConfig) error {
	cfg, err := w.service.ResolveEmailConfigForTest(ctx, input, "smtp")
	if err != nil {
		return err
	}
	if w.email == nil {
		return errors.New("group application email sender is unavailable")
	}
	return w.email.TestSMTPConnectionWithConfig(cfg.SMTPTransportConfig())
}

func (w *GroupApplicationWorker) SendTestEmail(ctx context.Context, input GroupApplicationEmailConfig, recipient string) error {
	recipient, err := NormalizeGroupApplicationEmail(recipient)
	if err != nil {
		return err
	}
	cfg, err := w.service.ResolveEmailConfigForTest(ctx, input, "smtp")
	if err != nil {
		return err
	}
	if w.email == nil {
		return errors.New("group application email sender is unavailable")
	}
	return w.email.SendEmailWithConfigAndOptions(
		cfg.SMTPTransportConfig(), recipient,
		"Sub2API - group application email test",
		"<p>This is a test message from the standalone group application email configuration.</p>",
		EmailSendOptions{},
	)
}

func (w *GroupApplicationWorker) TestIMAP(ctx context.Context, input GroupApplicationEmailConfig) ([]string, error) {
	testCtx, cancel := context.WithTimeout(ctx, groupApplicationIMAPTestTimeout)
	defer cancel()
	cfg, err := w.service.ResolveEmailConfigForTest(testCtx, input, "imap")
	if err != nil {
		return nil, err
	}
	client, err := openGroupApplicationIMAPClient(testCtx, &cfg.IMAP)
	if client != nil {
		defer client.Close()
	}
	if err != nil {
		return nil, groupApplicationIMAPTestError(err)
	}
	mailboxes, err := client.List("", "*", nil).Collect()
	if err != nil {
		if testCtx.Err() != nil {
			err = testCtx.Err()
		}
		return nil, groupApplicationIMAPTestError(newGroupApplicationIMAPOperationError("list", err))
	}
	return groupApplicationMailboxNames(mailboxes), nil
}

type groupApplicationIMAPOperationError struct {
	operation string
	err       error
}

func newGroupApplicationIMAPOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &groupApplicationIMAPOperationError{operation: operation, err: err}
}

func (e *groupApplicationIMAPOperationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s IMAP: %v", e.operation, e.err)
}

func (e *groupApplicationIMAPOperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func groupApplicationIMAPTestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return infraerrors.BadRequest(
			"GROUP_APPLICATION_IMAP_TIMEOUT",
			"The IMAP server did not respond within the 10-second test window. Verify the host, port, TLS mode, and network route.",
		).WithCause(err)
	}

	var operationErr *groupApplicationIMAPOperationError
	if !errors.As(err, &operationErr) {
		return infraerrors.BadRequest(
			"GROUP_APPLICATION_IMAP_TEST_FAILED",
			"IMAP test failed. Verify the server settings and account permissions.",
		).WithCause(err)
	}

	var reason, message string
	switch operationErr.operation {
	case "connect":
		reason = "GROUP_APPLICATION_IMAP_CONNECT_FAILED"
		message = "Could not connect to the IMAP server. Verify the host, port, TLS mode, DNS, and outbound network access."
	case "login":
		reason = "GROUP_APPLICATION_IMAP_LOGIN_FAILED"
		message = "IMAP login failed. Verify the full email address, enable IMAP access, and use a client-specific password when secure login is enabled."
	case "list":
		reason = "GROUP_APPLICATION_IMAP_LIST_FAILED"
		message = "IMAP login succeeded, but mailbox folders could not be listed. Verify that the account has IMAP folder access."
	default:
		reason = "GROUP_APPLICATION_IMAP_TEST_FAILED"
		message = "IMAP test failed. Verify the server settings and account permissions."
	}
	return infraerrors.BadRequest(reason, message).WithCause(err)
}

func groupApplicationMailboxNames(mailboxes []*imap.ListData) []string {
	unique := make(map[string]string, len(mailboxes))
	for _, mailbox := range mailboxes {
		selectable := true
		for _, attribute := range mailbox.Attrs {
			if attribute == imap.MailboxAttrNoSelect || attribute == imap.MailboxAttrNonExistent {
				selectable = false
				break
			}
		}
		if !selectable {
			continue
		}
		name := strings.TrimSpace(mailbox.Mailbox)
		if name != "" {
			key := strings.ToLower(name)
			if _, exists := unique[key]; !exists {
				unique[key] = name
			}
		}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, unique[name])
	}
	sort.Slice(names, func(i, j int) bool {
		if strings.EqualFold(names[i], "INBOX") {
			return true
		}
		if strings.EqualFold(names[j], "INBOX") {
			return false
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

type groupApplicationIMAPClient struct {
	*imapclient.Client
	stopContextClose func() bool
}

func (c *groupApplicationIMAPClient) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	if c.stopContextClose != nil {
		c.stopContextClose()
	}
	return c.Client.Close()
}

func authenticateGroupApplicationIMAPClient(client *imapclient.Client, username, password string) error {
	caps := client.Caps()
	if caps == nil {
		return errors.New("could not read IMAP server capabilities")
	}
	if caps.Has(imap.AuthCap(sasl.Plain)) {
		return client.Authenticate(sasl.NewPlainClient("", username, password))
	}
	if caps.Has(imap.CapLoginDisabled) {
		return errors.New("IMAP server does not advertise a supported password authentication mechanism")
	}
	return client.Login(username, password).Wait()
}

func openGroupApplicationIMAPClient(ctx context.Context, cfg *GroupApplicationIMAPConfig) (*groupApplicationIMAPClient, error) {
	if cfg == nil {
		return nil, errors.New("missing IMAP config")
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	options := &imapclient.Options{TLSConfig: tlsConfig}
	rawConn, err := (&net.Dialer{Timeout: groupApplicationIMAPDialTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, newGroupApplicationIMAPOperationError("connect", err)
	}
	stopContextClose := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	var client *imapclient.Client
	if cfg.TLSMode == "starttls" {
		client, err = imapclient.NewStartTLS(rawConn, options)
	} else {
		tlsConfig.NextProtos = []string{"imap"}
		tlsConn := tls.Client(rawConn, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err == nil {
			client = imapclient.New(tlsConn, options)
		}
	}
	if err != nil {
		stopContextClose()
		_ = rawConn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, newGroupApplicationIMAPOperationError("connect", err)
	}
	wrapped := &groupApplicationIMAPClient{Client: client, stopContextClose: stopContextClose}
	if err = client.WaitGreeting(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		wrapped.Close()
		return nil, newGroupApplicationIMAPOperationError("connect", err)
	}
	if err = authenticateGroupApplicationIMAPClient(client, cfg.Username, cfg.Password); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		wrapped.Close()
		return nil, newGroupApplicationIMAPOperationError("login", err)
	}
	return wrapped, nil
}

func openGroupApplicationMailbox(ctx context.Context, cfg *GroupApplicationIMAPConfig) (*groupApplicationIMAPClient, *imap.SelectData, error) {
	client, err := openGroupApplicationIMAPClient(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	selectData, err := client.Select(cfg.Mailbox, nil).Wait()
	if err != nil {
		client.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, newGroupApplicationIMAPOperationError("select", err)
	}
	return client, selectData, nil
}

func groupApplicationMailboxFingerprint(cfg *GroupApplicationIMAPConfig) string {
	sum := sha256.Sum256([]byte(strings.ToLower(cfg.Host) + "\n" + strings.ToLower(cfg.Username) + "\n" + cfg.Mailbox))
	return hex.EncodeToString(sum[:])
}

func (w *GroupApplicationWorker) pollIMAP(ctx context.Context, cfg *GroupApplicationIMAPConfig) error {
	client, selected, err := openGroupApplicationMailbox(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	w.lastIMAPCheck.Store(time.Now().UTC())
	fingerprint := groupApplicationMailboxFingerprint(cfg)
	last, exists, err := w.repo.MaxProcessedUID(ctx, fingerprint, selected.UIDValidity)
	if err != nil {
		return err
	}
	if !exists {
		start := uint32(0)
		if selected.UIDNext > 0 {
			start = uint32(selected.UIDNext) - 1
		}
		_, err = w.repo.StoreReceipt(ctx, GroupApplicationReceipt{MailboxFingerprint: fingerprint, UIDValidity: selected.UIDValidity, UID: start, Result: "cursor_start"})
		return err
	}
	if selected.UIDNext == 0 || uint32(selected.UIDNext) <= last+1 {
		return nil
	}
	section := &imap.FetchItemBodySection{Peek: true}
	set := imap.UIDSet{}
	set.AddRange(imap.UID(last+1), imap.UID(0))
	messages, err := client.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return fmt.Errorf("fetch IMAP replies: %w", err)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].UID < messages[j].UID })
	for _, message := range messages {
		uid := uint32(message.UID)
		receipt := GroupApplicationReceipt{MailboxFingerprint: fingerprint, UIDValidity: selected.UIDValidity, UID: uid, Result: "ignored"}
		if message.RFC822Size > 5<<20 {
			receipt.Result = "too_large"
			_, err = w.repo.StoreReceipt(ctx, receipt)
			if err != nil {
				return err
			}
			continue
		}
		raw := message.FindBodySection(section)
		parsed, parseErr := parseGroupApplicationReply(raw)
		if parseErr != nil {
			receipt.Result = "parse_error"
			_, err = w.repo.StoreReceipt(ctx, receipt)
			if err != nil {
				return err
			}
			continue
		}
		receipt.MessageID = parsed.MessageID
		receipt.FromAddress = parsed.From
		receipt.InReplyTo = parsed.InReplyTo
		receipt.References = parsed.References
		receipt.ReplySHA256 = GroupApplicationReplyDigest(parsed.Reply)
		ids := messageIDCandidates(parsed.InReplyTo, parsed.References)
		match, matchErr := w.repo.FindApprovalByMessageIDs(ctx, ids)
		if matchErr != nil {
			if !errors.Is(matchErr, ErrGroupApplicationNotFound) {
				return matchErr
			}
			receipt.Result = "unrelated"
			_, err = w.repo.StoreReceipt(ctx, receipt)
			if err != nil {
				return err
			}
			continue
		}
		receipt.ApplicationID = &match.Application.ID
		receipt.EncryptedContent, receipt.ContentTruncated, err = w.service.protectInboundCommunication(parsed.Subject, parsed.Reply)
		if err != nil {
			return err
		}
		result, processErr := w.service.ProcessInboundReply(ctx, match.Application.ID, parsed.From, parsed.Reply)
		if processErr != nil && !errors.Is(processErr, ErrGroupApplicationState) {
			return processErr
		}
		receipt.Result = result
		if _, err = w.repo.StoreReceipt(ctx, receipt); err != nil {
			return err
		}
		w.repliesProcessed.Add(1)
	}
	return nil
}

type parsedGroupApplicationReply struct{ MessageID, From, InReplyTo, References, Subject, Reply string }

func parseGroupApplicationReply(raw []byte) (parsedGroupApplicationReply, error) {
	reader, err := mailmessage.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return parsedGroupApplicationReply{}, err
	}
	defer reader.Close()
	result := parsedGroupApplicationReply{
		MessageID:  strings.TrimSpace(reader.Header.Get("Message-ID")),
		InReplyTo:  strings.TrimSpace(reader.Header.Get("In-Reply-To")),
		References: strings.TrimSpace(reader.Header.Get("References")),
		Subject:    strings.TrimSpace(reader.Header.Get("Subject")),
	}
	addresses, err := reader.Header.AddressList("From")
	if err == nil && len(addresses) > 0 {
		result.From = addresses[0].Address
	}
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			return parsedGroupApplicationReply{}, partErr
		}
		inline, ok := part.Header.(*mailmessage.InlineHeader)
		if !ok {
			continue
		}
		contentType, _, _ := inline.ContentType()
		if !strings.EqualFold(contentType, "text/plain") {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(part.Body, 2<<20))
		if readErr != nil {
			return parsedGroupApplicationReply{}, readErr
		}
		result.Reply = extractNewestGroupApplicationReply(string(body))
		break
	}
	return result, nil
}

func extractNewestGroupApplicationReply(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, ">") || strings.HasPrefix(lower, "on ") && strings.HasSuffix(lower, " wrote:") || strings.Contains(trimmed, "写道：") || strings.EqualFold(trimmed, "-----Original Message-----") {
			break
		}
		kept = append(kept, line)
	}
	return NormalizeGroupApplicationReply(strings.Join(kept, "\n"))
}
