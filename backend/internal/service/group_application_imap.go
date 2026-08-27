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
	"github.com/google/uuid"
)

type GroupApplicationIMAPConfig struct {
	Enabled             bool   `json:"enabled"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"password,omitempty"`
	PasswordConfigured  bool   `json:"password_configured"`
	Mailbox             string `json:"mailbox"`
	ReplyAddress        string `json:"reply_address"`
	TLSMode             string `json:"tls_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type storedGroupApplicationIMAPConfig struct {
	Enabled             bool   `json:"enabled"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	EncryptedPassword   string `json:"encrypted_password"`
	Mailbox             string `json:"mailbox"`
	ReplyAddress        string `json:"reply_address"`
	TLSMode             string `json:"tls_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

func defaultGroupApplicationIMAPConfig() GroupApplicationIMAPConfig {
	return GroupApplicationIMAPConfig{Port: 993, Mailbox: "INBOX", TLSMode: "implicit", PollIntervalSeconds: 60}
}

func (s *GroupApplicationService) GetIMAPConfig(ctx context.Context) (*GroupApplicationIMAPConfig, error) {
	stored, err := s.loadStoredIMAPConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &GroupApplicationIMAPConfig{
		Enabled: stored.Enabled, Host: stored.Host, Port: stored.Port, Username: stored.Username,
		PasswordConfigured: stored.EncryptedPassword != "", Mailbox: stored.Mailbox,
		ReplyAddress: stored.ReplyAddress, TLSMode: stored.TLSMode,
		PollIntervalSeconds: stored.PollIntervalSeconds,
	}, nil
}

func (s *GroupApplicationService) SaveIMAPConfig(ctx context.Context, input GroupApplicationIMAPConfig) (*GroupApplicationIMAPConfig, error) {
	if s.settingRepo == nil || s.encryptor == nil {
		return nil, errors.New("group application IMAP settings are unavailable")
	}
	existing, err := s.loadStoredIMAPConfig(ctx)
	if err != nil {
		return nil, err
	}
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	input.Mailbox = strings.TrimSpace(input.Mailbox)
	input.ReplyAddress = strings.TrimSpace(input.ReplyAddress)
	input.TLSMode = strings.ToLower(strings.TrimSpace(input.TLSMode))
	if input.Port == 0 {
		input.Port = 993
	}
	if input.Mailbox == "" {
		input.Mailbox = "INBOX"
	}
	if input.TLSMode == "" {
		input.TLSMode = "implicit"
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 60
	}
	password := strings.TrimSpace(input.Password)
	encryptedPassword := existing.EncryptedPassword
	if password != "" && password != "********" {
		encryptedPassword, err = s.encryptor.Encrypt(password)
		if err != nil {
			return nil, fmt.Errorf("encrypt IMAP password: %w", err)
		}
	}
	stored := storedGroupApplicationIMAPConfig{
		Enabled: input.Enabled, Host: input.Host, Port: input.Port, Username: input.Username,
		EncryptedPassword: encryptedPassword, Mailbox: input.Mailbox,
		ReplyAddress: input.ReplyAddress, TLSMode: input.TLSMode,
		PollIntervalSeconds: input.PollIntervalSeconds,
	}
	if err := validateStoredGroupApplicationIMAPConfig(stored, input.Enabled); err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_IMAP_CONFIG", err.Error())
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGroupApplicationIMAP, string(raw)); err != nil {
		return nil, err
	}
	return s.GetIMAPConfig(ctx)
}

func (s *GroupApplicationService) loadStoredIMAPConfig(ctx context.Context) (storedGroupApplicationIMAPConfig, error) {
	defaults := defaultGroupApplicationIMAPConfig()
	stored := storedGroupApplicationIMAPConfig{Port: defaults.Port, Mailbox: defaults.Mailbox, TLSMode: defaults.TLSMode, PollIntervalSeconds: defaults.PollIntervalSeconds}
	if s.settingRepo == nil {
		return stored, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGroupApplicationIMAP)
	if err != nil || strings.TrimSpace(raw) == "" {
		return stored, nil
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return storedGroupApplicationIMAPConfig{}, fmt.Errorf("decode IMAP settings: %w", err)
	}
	if stored.Port == 0 {
		stored.Port = defaults.Port
	}
	if stored.Mailbox == "" {
		stored.Mailbox = defaults.Mailbox
	}
	if stored.TLSMode == "" {
		stored.TLSMode = defaults.TLSMode
	}
	if stored.PollIntervalSeconds == 0 {
		stored.PollIntervalSeconds = defaults.PollIntervalSeconds
	}
	return stored, nil
}

func (s *GroupApplicationService) LoadIMAPConfig(ctx context.Context, requireEnabled bool) (*GroupApplicationIMAPConfig, error) {
	stored, err := s.loadStoredIMAPConfig(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateStoredGroupApplicationIMAPConfig(stored, requireEnabled); err != nil {
		return nil, err
	}
	password, err := s.encryptor.Decrypt(stored.EncryptedPassword)
	if err != nil {
		return nil, fmt.Errorf("decrypt IMAP password: %w", err)
	}
	return &GroupApplicationIMAPConfig{
		Enabled: stored.Enabled, Host: stored.Host, Port: stored.Port, Username: stored.Username,
		Password: password, PasswordConfigured: true, Mailbox: stored.Mailbox,
		ReplyAddress: stored.ReplyAddress, TLSMode: stored.TLSMode,
		PollIntervalSeconds: stored.PollIntervalSeconds,
	}, nil
}

func validateStoredGroupApplicationIMAPConfig(config storedGroupApplicationIMAPConfig, requireEnabled bool) error {
	if requireEnabled && !config.Enabled {
		return errors.New("IMAP reply processing is disabled")
	}
	if !config.Enabled && !requireEnabled {
		return nil
	}
	if config.Host == "" || config.Username == "" || config.EncryptedPassword == "" || config.ReplyAddress == "" {
		return errors.New("host, username, password and reply address are required")
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("IMAP port must be between 1 and 65535")
	}
	if config.TLSMode != "implicit" && config.TLSMode != "starttls" {
		return errors.New("TLS mode must be implicit or starttls")
	}
	if config.PollIntervalSeconds < 15 || config.PollIntervalSeconds > 300 {
		return errors.New("poll interval must be between 15 and 300 seconds")
	}
	if _, err := NormalizeGroupApplicationEmail(config.ReplyAddress); err != nil {
		return errors.New("invalid IMAP reply address")
	}
	return nil
}

type GroupApplicationWorkerHealth struct {
	Running          bool      `json:"running"`
	MailProcessed    uint64    `json:"mail_processed"`
	MailFailures     uint64    `json:"mail_failures"`
	RepliesProcessed uint64    `json:"replies_processed"`
	ReplyFailures    uint64    `json:"reply_failures"`
	LastIMAPCheckAt  time.Time `json:"last_imap_check_at,omitempty"`
	LastIMAPError    string    `json:"last_imap_error,omitempty"`
}

type GroupApplicationWorker struct {
	repo             GroupApplicationRepository
	service          *GroupApplicationService
	email            *EmailService
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
}

func NewGroupApplicationWorker(repo GroupApplicationRepository, service *GroupApplicationService, email *EmailService) *GroupApplicationWorker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &GroupApplicationWorker{repo: repo, service: service, email: email, workerID: uuid.NewString(), ctx: ctx, cancel: cancel}
	w.lastIMAPCheck.Store(time.Time{})
	w.lastIMAPError.Store("")
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
	health := GroupApplicationWorkerHealth{Running: w.running.Load(), MailProcessed: w.mailProcessed.Load(), MailFailures: w.mailFailures.Load(), RepliesProcessed: w.repliesProcessed.Load(), ReplyFailures: w.replyFailures.Load()}
	if value, ok := w.lastIMAPCheck.Load().(time.Time); ok {
		health.LastIMAPCheckAt = value
	}
	if value, ok := w.lastIMAPError.Load().(string); ok {
		health.LastIMAPError = value
	}
	return health
}

func (w *GroupApplicationWorker) run() {
	defer w.wg.Done()
	defer w.running.Store(false)
	mailTicker := time.NewTicker(time.Second)
	defer mailTicker.Stop()
	nextIMAP := time.Now()
	for {
		if err := w.processMailBatch(w.ctx); err != nil && w.ctx.Err() == nil {
			w.mailFailures.Add(1)
			slog.Warn("group application mail outbox failed", "error", err)
		}
		if !time.Now().Before(nextIMAP) {
			interval := 60 * time.Second
			if cfg, err := w.service.LoadIMAPConfig(w.ctx, false); err == nil && cfg.Enabled {
				interval = time.Duration(cfg.PollIntervalSeconds) * time.Second
				if err := w.pollIMAP(w.ctx, cfg); err != nil && w.ctx.Err() == nil {
					w.replyFailures.Add(1)
					w.lastIMAPError.Store(boundedGroupApplicationError(err))
					slog.Warn("group application IMAP poll failed", "error", err)
				} else {
					w.lastIMAPError.Store("")
				}
			}
			nextIMAP = time.Now().Add(interval)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-mailTicker.C:
		}
	}
}

func (w *GroupApplicationWorker) processMailBatch(ctx context.Context) error {
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
			var cfg *GroupApplicationIMAPConfig
			cfg, sendErr = w.service.LoadIMAPConfig(ctx, true)
			if sendErr == nil {
				options.ReplyTo = cfg.ReplyAddress
			}
		}
		if sendErr == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			sendErr = w.email.SendEmailWithOptions(sendCtx, job.Recipient, job.Subject, job.HTMLBody, options)
			cancel()
		}
		if sendErr == nil {
			if ackErr := w.repo.MarkMailSent(ctx, job.ID, w.workerID); ackErr != nil {
				return ackErr
			}
			w.mailProcessed.Add(1)
			continue
		}
		w.mailFailures.Add(1)
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

func (w *GroupApplicationWorker) TestIMAP(ctx context.Context) error {
	cfg, err := w.service.LoadIMAPConfig(ctx, true)
	if err != nil {
		return err
	}
	client, _, err := openGroupApplicationMailbox(ctx, cfg)
	if client != nil {
		defer client.Close()
	}
	return err
}

func openGroupApplicationMailbox(ctx context.Context, cfg *GroupApplicationIMAPConfig) (*imapclient.Client, *imap.SelectData, error) {
	if cfg == nil {
		return nil, nil, errors.New("missing IMAP config")
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	options := &imapclient.Options{TLSConfig: &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}, Dialer: &net.Dialer{Timeout: 15 * time.Second}}
	var client *imapclient.Client
	var err error
	if cfg.TLSMode == "starttls" {
		client, err = imapclient.DialStartTLS(address, options)
	} else {
		client, err = imapclient.DialTLS(address, options)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("connect IMAP: %w", err)
	}
	if err = client.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("login IMAP: %w", err)
	}
	selectData, err := client.Select(cfg.Mailbox, nil).Wait()
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("select IMAP mailbox: %w", err)
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
