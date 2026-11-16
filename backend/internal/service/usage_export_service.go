package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const usageExportMaxBytes int64 = 256 << 20

var errUsageExportAccess = infraerrors.Forbidden("USAGE_EXPORT_ACCESS_DENIED", "Export permission is no longer valid")

type UsageExportService struct {
	repo         UsageExportRepository
	source       UsageExportSource
	users        UserRepository
	settings     *SettingService
	dir          string
	maxRows      int64
	ctx          context.Context
	stop         context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	mu           sync.Mutex
	activeID     string
	activeCancel context.CancelFunc
	creating     map[int64]struct{}
}

func NewUsageExportService(repo UsageExportRepository, usage UsageLogRepository, users UserRepository, settings *SettingService, cfg *config.Config) *UsageExportService {
	dir := "/app/data/usage-exports"
	maxRows := int64(200000)
	if cfg != nil {
		if cfg.UsageExport.Directory != "" {
			dir = cfg.UsageExport.Directory
		}
		if cfg.UsageExport.MaxRows > 0 {
			maxRows = cfg.UsageExport.MaxRows
		}
	}
	if maxRows > 1048575 {
		maxRows = 1048575
	}
	source, _ := usage.(UsageExportSource)
	ctx, cancel := context.WithCancel(context.Background())
	return &UsageExportService{repo: repo, source: source, users: users, settings: settings, dir: dir, maxRows: maxRows, ctx: ctx, stop: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
}
func exportToken() (string, error) {
	var b [24]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func exportDigest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func exportKeyRevision(k *ScopedAdminKey) string {
	return exportDigest(k.Hash + string(usageExportJSON(k.Policy)) + string(usageExportJSON(k.AllowedIPs)))
}
func (s *UsageExportService) Owner(ctx context.Context, userID int64, method, ip string) (UsageExportOwner, error) {
	owner := UsageExportOwner{UserID: userID, Kind: "jwt", ClientIP: ip}
	if p := adminaccess.FromContext(ctx); p != nil {
		r, e := s.settings.scopedAdminKeys()
		if e != nil {
			return owner, e
		}
		k, e := r.GetScopedAdminKey(ctx, p.KeyID)
		if e != nil {
			return owner, e
		}
		owner.UserID = k.CreatedBy
		owner.Kind = "scoped_key"
		owner.KeyID = k.ID
		owner.Revision = exportKeyRevision(k)
		owner.Restricted = !k.Policy.FullAccess
	} else if method == "admin_api_key" {
		key, e := s.settings.GetAdminAPIKey(ctx)
		if e != nil {
			return owner, e
		}
		owner.Kind = "admin_key"
		owner.Revision = exportDigest(key)
	} else {
		u, e := s.users.GetByID(ctx, userID)
		if e != nil {
			return owner, e
		}
		owner.Revision = strconv.FormatInt(resolvedTokenVersion(u), 10)
	}
	return owner, s.checkOwner(ctx, owner, ip)
}
func (s *UsageExportService) checkOwner(ctx context.Context, o UsageExportOwner, ip string) error {
	u, err := s.users.GetByID(ctx, o.UserID)
	if err != nil || u == nil || !u.IsAdmin() || !u.IsActive() {
		return errUsageExportAccess
	}
	switch o.Kind {
	case "scoped_key":
		r, e := s.settings.scopedAdminKeys()
		if e != nil {
			return e
		}
		k, e := r.GetScopedAdminKey(ctx, o.KeyID)
		if e != nil || k == nil || !k.Enabled || k.RevokedAt != nil || (k.ExpiresAt != nil && !k.ExpiresAt.After(time.Now())) || k.CreatedBy != o.UserID || exportKeyRevision(k) != o.Revision || !scopedAdminIPAllowed(k.AllowedIPs, ip) || (!k.Policy.FullAccess && !slices.Contains(k.Policy.GlobalPermissions, "usage.read")) {
			return errUsageExportAccess
		}
	case "admin_key":
		key, e := s.settings.GetAdminAPIKey(ctx)
		if e != nil || key == "" || exportDigest(key) != o.Revision {
			return errUsageExportAccess
		}
	case "jwt":
		if strconv.FormatInt(resolvedTokenVersion(u), 10) != o.Revision {
			return errUsageExportAccess
		}
	default:
		return errUsageExportAccess
	}
	return nil
}
func sameExportOwner(a, b UsageExportOwner) bool {
	return a.UserID == b.UserID && a.Kind == b.Kind && a.KeyID == b.KeyID && a.Revision == b.Revision
}
func (s *UsageExportService) owned(ctx context.Context, o UsageExportOwner, id string) (*UsageExportJob, error) {
	j, e := s.repo.Get(ctx, id)
	if e != nil || !sameExportOwner(j.Owner, o) {
		return nil, errUsageExportAccess
	}
	return j, s.checkOwner(ctx, o, o.ClientIP)
}
func (s *UsageExportService) Create(ctx context.Context, o UsageExportOwner, spec UsageExportSpec) (*UsageExportJob, error) {
	if s.source == nil {
		return nil, errors.New("usage export source unavailable")
	}
	if e := s.checkOwner(ctx, o, o.ClientIP); e != nil {
		return nil, e
	}
	// Do not run an expensive exact count for concurrent submissions by the
	// same owner. The repository still enforces the final cross-instance limit.
	s.mu.Lock()
	if _, exists := s.creating[o.UserID]; exists {
		s.mu.Unlock()
		return nil, infraerrors.Conflict("USAGE_EXPORT_QUEUE_FULL", "An export is already being prepared")
	}
	if s.creating == nil {
		s.creating = make(map[int64]struct{})
	}
	s.creating[o.UserID] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.creating, o.UserID); s.mu.Unlock() }()
	if e := s.repo.CanCreate(ctx, o.UserID); e != nil {
		return nil, usageExportCreateError(e)
	}
	if e := os.MkdirAll(s.dir, 0700); e != nil {
		return nil, e
	}
	if e := s.checkDisk(); e != nil {
		return nil, e
	}
	if spec.SortBy != "model" && spec.SortBy != "id" {
		spec.SortBy = "created_at"
	}
	if spec.SortOrder != "asc" {
		spec.SortOrder = "desc"
	}
	if spec.Timezone == "" {
		spec.Timezone = "UTC"
	}
	if _, e := time.LoadLocation(spec.Timezone); e != nil {
		return nil, infraerrors.BadRequest("INVALID_TIMEZONE", "Invalid timezone")
	}
	if spec.Language != "zh" {
		spec.Language = "en"
	}
	query, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	upper, total, e := s.source.PrepareUsageExport(query, spec)
	if e != nil {
		return nil, e
	}
	if total > s.maxRows {
		return nil, infraerrors.BadRequest("USAGE_EXPORT_TOO_MANY_ROWS", fmt.Sprintf("Export exceeds %d rows; narrow the date range", s.maxRows))
	}
	id, e := exportToken()
	if e != nil {
		return nil, e
	}
	j := &UsageExportJob{ID: id, Owner: o, Spec: spec, MaxID: upper, TotalRows: total}
	if e = s.repo.Create(ctx, j); e != nil {
		return nil, usageExportCreateError(e)
	}
	s.notify()
	return j, nil
}
func usageExportCreateError(err error) error {
	if err.Error() == "USAGE_EXPORT_QUEUE_FULL" {
		return infraerrors.Conflict("USAGE_EXPORT_QUEUE_FULL", "The export queue or owner limit is full")
	}
	return err
}
func (s *UsageExportService) List(ctx context.Context, o UsageExportOwner) ([]UsageExportJob, error) {
	return s.repo.List(ctx, o)
}
func (s *UsageExportService) Get(ctx context.Context, o UsageExportOwner, id string) (*UsageExportJob, error) {
	return s.owned(ctx, o, id)
}
func (s *UsageExportService) Cancel(ctx context.Context, o UsageExportOwner, id string) error {
	if _, e := s.owned(ctx, o, id); e != nil {
		return e
	}
	if e := s.repo.Cancel(ctx, id); e != nil {
		return e
	}
	s.mu.Lock()
	if s.activeID == id && s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	return nil
}
func (s *UsageExportService) Delete(ctx context.Context, o UsageExportOwner, id string) error {
	if _, e := s.owned(ctx, o, id); e != nil {
		return e
	}
	return s.delete(ctx, id)
}
func (s *UsageExportService) delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id, s.removeArtifactFiles)
}
func (s *UsageExportService) removeArtifactFiles(a UsageExportArtifact) error {
	for _, key := range []string{a.FileKey + ".tmp", a.FileKey} {
		if err := os.Remove(s.path(key)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (s *UsageExportService) Ticket(ctx context.Context, o UsageExportOwner, id string) (string, error) {
	if _, e := s.owned(ctx, o, id); e != nil {
		return "", e
	}
	token, e := exportToken()
	if e != nil {
		return "", e
	}
	e = s.repo.SetTicket(ctx, id, exportDigest(token))
	return token, e
}
func (s *UsageExportService) Download(ctx context.Context, ticket, ip string) (*UsageExportDownload, *UsageExportJob, error) {
	if len(ticket) != 48 {
		return nil, nil, errUsageExportAccess
	}
	hash := exportDigest(ticket)
	preview, e := s.repo.PeekByTicket(ctx, hash)
	if e != nil {
		return nil, nil, errUsageExportAccess
	}
	if e = s.checkOwner(ctx, preview.Owner, ip); e != nil {
		return nil, nil, e
	}
	var f *os.File
	started := time.Now()
	openCtx, stopOpen := context.WithTimeout(ctx, 5*time.Second)
	defer stopOpen()
	j, e := s.repo.GetByTicket(openCtx, hash, func(j *UsageExportJob) error {
		if !sameExportOwner(j.Owner, preview.Owner) {
			return errUsageExportAccess
		}
		var err error
		f, err = os.Open(s.path(j.FileKey))
		return err
	})
	if e != nil {
		if f != nil {
			_ = f.Close()
		}
		return nil, nil, errUsageExportAccess
	}
	return newUsageExportDownload(ctx, f, s.repo, j.ID, j.ClaimToken, hash, started.Add(50*time.Second)), j, nil
}
func (s *UsageExportService) path(key string) string { return filepath.Join(s.dir, filepath.Base(key)) }
func (s *UsageExportService) checkDisk() error {
	free, e := usageExportFreeDisk(s.dir)
	if e != nil {
		return e
	}
	if free < 1<<30 {
		return errors.New("USAGE_EXPORT_DISK_SPACE")
	}
	return nil
}
func (s *UsageExportService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *UsageExportService) Start() { go s.loop() }
func (s *UsageExportService) Stop()  { s.stop(); <-s.done }
func (s *UsageExportService) loop() {
	defer close(s.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cleanup := time.Now()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		if time.Since(cleanup) >= time.Minute {
			s.cleanup()
			cleanup = time.Now()
		}
		if e := os.MkdirAll(s.dir, 0700); e != nil {
			continue
		}
		token, e := exportToken()
		if e != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		j, e := s.repo.Claim(ctx, token, usageExportMaxBytes)
		cancel()
		if e != nil {
			continue
		}
		s.run(j)
		s.notify()
	}
}
func (s *UsageExportService) run(j *UsageExportJob) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	s.mu.Lock()
	s.activeID = j.ID
	s.activeCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.activeID = ""; s.activeCancel = nil; s.mu.Unlock() }()
	var progress atomic.Int64
	var leaseDeadline atomic.Int64
	deadline := time.Now().Add(50 * time.Second)
	if j.LeaseUntil != nil && j.LeaseUntil.Before(deadline) {
		deadline = *j.LeaseUntil
	}
	leaseDeadline.Store(deadline.UnixNano())
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().UnixNano() >= leaseDeadline.Load() {
			return context.DeadlineExceeded
		}
		return nil
	}
	hbDone := make(chan struct{})
	hbCtx, hbCancel := context.WithCancel(ctx)
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				// Renew from call start with margin; a delayed/lost reply cannot
				// extend the local write window beyond the server lease.
				renewedUntil := time.Now().Add(50 * time.Second)
				beat, stopBeat := context.WithTimeout(hbCtx, 5*time.Second)
				ok, e := s.repo.Heartbeat(beat, j.ID, j.ClaimToken, progress.Load())
				stopBeat()
				if e != nil || !ok {
					cancel()
					return
				}
				leaseDeadline.Store(renewedUntil.UnixNano())
			}
		}
	}()
	key := j.ID + "-" + j.ClaimToken + ".xlsx"
	tmp := s.path(key + ".tmp")
	err := s.generateChecked(ctx, j, tmp, &progress, check)
	if err == nil {
		err = check()
	}
	if err == nil {
		err = os.Rename(tmp, s.path(key))
	}
	hbCancel()
	<-hbDone
	j.RowsWritten = progress.Load()
	j.Status = "completed"
	j.FileKey = key
	if err != nil {
		j.Status = "failed"
		j.FileKey = ""
		j.SizeBytes = 0
		j.ErrorCode = "GENERATION_FAILED"
		switch err.Error() {
		case "USAGE_EXPORT_TOO_MANY_ROWS", "USAGE_EXPORT_FILE_TOO_LARGE", "USAGE_EXPORT_DISK_SPACE":
			j.ErrorCode = err.Error()
		}
		if errors.Is(err, errUsageExportAccess) {
			j.ErrorCode = "ACCESS_REVOKED"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			j.ErrorCode = "TIMEOUT"
		}
		if errors.Is(err, context.Canceled) {
			j.ErrorCode = "CANCELLED"
		}
		slog.Warn("usage export failed", "id", j.ID, "error", err)
	}
	finish, stop := context.WithTimeout(context.Background(), 10*time.Second)
	ok, e := s.repo.Finish(finish, j)
	stop()
	if e != nil {
		// Commit may have succeeded before the connection failed. Cleanup checks
		// the persisted state before releasing the reservation or removing files.
		slog.Warn("usage export finish uncertain", "id", j.ID, "error", e)
		return
	}
	if err != nil || !ok {
		cleanup, stopCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCleanup()
		_ = s.repo.ReleaseArtifact(cleanup, UsageExportArtifact{JobID: j.ID, ClaimToken: j.ClaimToken, FileKey: key}, s.removeArtifactFiles)
	}
}
func (s *UsageExportService) generate(ctx context.Context, j *UsageExportJob, tmp string, progress *atomic.Int64) error {
	return s.generateChecked(ctx, j, tmp, progress, ctx.Err)
}
func (s *UsageExportService) generateChecked(ctx context.Context, j *UsageExportJob, tmp string, progress *atomic.Int64, check func() error) error {
	if e := s.checkOwner(ctx, j.Owner, j.Owner.ClientIP); e != nil {
		return e
	}
	if e := check(); e != nil {
		return e
	}
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	limited := &usageExportLimitWriter{w: f, max: usageExportMaxBytes, ctx: ctx, check: check}
	writer, e := newUsageXLSX(limited, j.Spec.Language)
	if e != nil {
		return e
	}
	var cursor *UsageExportCursor
	for {
		if e = check(); e != nil {
			return e
		}
		if e = s.checkDisk(); e != nil {
			return e
		}
		logs, next, err := s.source.ReadUsageExport(ctx, j.Spec, j.MaxID, cursor, 1000, j.Owner.Restricted)
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			break
		}
		for i := range logs {
			if err = check(); err != nil {
				return err
			}
			if progress.Load() >= s.maxRows {
				return errors.New("USAGE_EXPORT_TOO_MANY_ROWS")
			}
			if err = writer.WriteRow(usageExportRow(&logs[i], j.Spec)); err != nil {
				return err
			}
			progress.Add(1)
		}
		cursor = next
		if next == nil {
			break
		}
	}
	if e = writer.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	j.SizeBytes = limited.n
	return nil
}

type usageExportLimitWriter struct {
	w      io.Writer
	n, max int64
	ctx    context.Context
	check  func() error
}

func (w *usageExportLimitWriter) Write(p []byte) (int, error) {
	if w.check != nil {
		if err := w.check(); err != nil {
			return 0, err
		}
	} else if w.ctx != nil {
		if err := w.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if int64(len(p)) > w.max-w.n {
		return 0, errors.New("USAGE_EXPORT_FILE_TOO_LARGE")
	}
	n, e := w.w.Write(p)
	w.n += int64(n)
	return n, e
}
func (s *UsageExportService) cleanup() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	jobs, e := s.repo.Expired(ctx)
	if e == nil {
		for _, j := range jobs {
			_ = s.delete(ctx, j.ID)
		}
	}
	artifacts, e := s.repo.CleanupArtifacts(ctx)
	if e != nil {
		return
	}
	for _, a := range artifacts {
		if ctx.Err() != nil {
			return
		}
		_ = s.repo.ReleaseArtifact(ctx, a, s.removeArtifactFiles)
	}
}
