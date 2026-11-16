package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type exportTestUsers struct {
	UserRepository
	user *User
}

func (r exportTestUsers) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type exportTestRepo struct {
	UsageExportRepository
	job       *UsageExportJob
	finishes  int
	finishErr error
}

func (r *exportTestRepo) Get(context.Context, string) (*UsageExportJob, error) { return r.job, nil }
func (r *exportTestRepo) Finish(_ context.Context, j *UsageExportJob) (bool, error) {
	r.finishes++
	r.job = j
	return false, r.finishErr
}
func (r *exportTestRepo) ReleaseArtifact(_ context.Context, a UsageExportArtifact, remove func(UsageExportArtifact) error) error {
	return remove(a)
}

type exportTestSource struct {
	UsageExportSource
	calls      int
	restricted bool
	block      bool
}

func (s *exportTestSource) ReadUsageExport(ctx context.Context, _ UsageExportSpec, _ int64, _ *UsageExportCursor, limit int, restricted bool) ([]UsageLog, *UsageExportCursor, error) {
	s.calls++
	s.restricted = restricted
	if s.block {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if limit != 1000 {
		return nil, nil, errors.New("unbounded batch")
	}
	if s.calls > 1 {
		return nil, nil, nil
	}
	return []UsageLog{{ID: 1, CreatedAt: time.Unix(100, 0), Model: "test"}}, &UsageExportCursor{ID: 1}, nil
}
func exportTestService(t *testing.T, source UsageExportSource) *UsageExportService {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &UsageExportService{source: source, users: exportTestUsers{user: &User{ID: 1, Role: "admin", Status: "active", TokenVersion: 2, TokenVersionResolved: true}}, dir: t.TempDir(), maxRows: 200000, ctx: ctx, stop: cancel}
}
func TestUsageExportOwnerIsolation(t *testing.T) {
	a := UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}
	for _, b := range []UsageExportOwner{{UserID: 2, Kind: "jwt", Revision: "2"}, {UserID: 1, Kind: "scoped_key", Revision: "2"}, {UserID: 1, Kind: "jwt", Revision: "3"}} {
		require.False(t, sameExportOwner(a, b))
	}
	s := exportTestService(t, nil)
	require.NoError(t, s.checkOwner(context.Background(), a, ""))
	a.Revision = "1"
	require.ErrorIs(t, s.checkOwner(context.Background(), a, ""), errUsageExportAccess)
}

func TestUsageExportRawUserPasswordChangeRevokesOwner(t *testing.T) {
	s := exportTestService(t, nil)
	user := &User{ID: 1, Role: "admin", Status: "active", Email: "admin@example.test", PasswordHash: "old-hash"}
	s.users = exportTestUsers{user: user}
	owner, err := s.Owner(context.Background(), 1, "jwt", "")
	require.NoError(t, err)
	require.NoError(t, s.checkOwner(context.Background(), owner, ""))
	user.PasswordHash = "changed-hash"
	require.ErrorIs(t, s.checkOwner(context.Background(), owner, ""), errUsageExportAccess)
}
func TestUsageExportBoundedGeneration(t *testing.T) {
	source := &exportTestSource{}
	s := exportTestService(t, source)
	j := &UsageExportJob{Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2", Restricted: true}, Spec: UsageExportSpec{Language: "en"}}
	var progress atomic.Int64
	name := filepath.Join(s.dir, "job.tmp")
	require.NoError(t, s.generate(context.Background(), j, name, &progress))
	require.EqualValues(t, 1, progress.Load())
	require.Equal(t, 2, source.calls)
	require.True(t, source.restricted)
	require.Positive(t, j.SizeBytes)
	file, e := os.ReadFile(name)
	require.NoError(t, e)
	require.True(t, bytes.HasPrefix(file, []byte("PK")))
}
func TestUsageExportCancellationStopsQuery(t *testing.T) {
	source := &exportTestSource{block: true}
	s := exportTestService(t, source)
	j := &UsageExportJob{Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var progress atomic.Int64
	require.ErrorIs(t, s.generate(ctx, j, filepath.Join(s.dir, "cancel.tmp"), &progress), context.DeadlineExceeded)
	require.Zero(t, progress.Load())
}
func TestUsageExportSizeLimitDoesNotProduceTruncatedSuccess(t *testing.T) {
	var dst bytes.Buffer
	w := &usageExportLimitWriter{w: &dst, max: 4}
	n, e := w.Write([]byte("1234"))
	require.NoError(t, e)
	require.Equal(t, 4, n)
	n, e = w.Write([]byte("5"))
	require.Error(t, e)
	require.Zero(t, n)
	require.Equal(t, "1234", dst.String())
}
func TestUsageExportLostClaimDiscardsOwnFile(t *testing.T) {
	source := &exportTestSource{}
	s := exportTestService(t, source)
	repo := &exportTestRepo{}
	s.repo = repo
	j := &UsageExportJob{ID: strings.Repeat("a", 48), ClaimToken: strings.Repeat("b", 48), Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}}
	s.run(j)
	require.Equal(t, 1, repo.finishes)
	entries, e := os.ReadDir(s.dir)
	require.NoError(t, e)
	require.Empty(t, entries)
}

func TestUsageExportUncertainFinishRetainsPublishedFile(t *testing.T) {
	s := exportTestService(t, &exportTestSource{})
	repo := &exportTestRepo{finishErr: errors.New("connection closed during commit")}
	s.repo = repo
	j := &UsageExportJob{ID: "job", ClaimToken: "claim", Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}}
	s.run(j)
	require.Equal(t, 1, repo.finishes)
	entries, err := os.ReadDir(s.dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "job-claim.xlsx", entries[0].Name())
}
func TestUsageExportExpiredLeaseCannotStartWriting(t *testing.T) {
	source := &exportTestSource{}
	s := exportTestService(t, source)
	repo := &exportTestRepo{}
	s.repo = repo
	expired := time.Now().Add(-time.Second)
	j := &UsageExportJob{ID: "expired", ClaimToken: "claim", LeaseUntil: &expired, Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}}
	s.run(j)
	require.Zero(t, source.calls)
	require.Equal(t, "TIMEOUT", j.ErrorCode)
	entries, err := os.ReadDir(s.dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type exportPreflightRepo struct {
	UsageExportRepository
	err   error
	calls atomic.Int64
}

func (r *exportPreflightRepo) CanCreate(context.Context, int64) error { r.calls.Add(1); return r.err }
func (r *exportPreflightRepo) Create(_ context.Context, j *UsageExportJob) error {
	j.Status = "queued"
	return nil
}

type exportPreparingSource struct {
	UsageExportSource
	entered, release chan struct{}
	calls            atomic.Int64
}

func (s *exportPreparingSource) PrepareUsageExport(ctx context.Context, _ UsageExportSpec) (int64, int64, error) {
	s.calls.Add(1)
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		}
	}
	return 1, 1, nil
}
func TestUsageExportPreflightRejectsBeforeExactCount(t *testing.T) {
	source := &exportPreparingSource{}
	s := exportTestService(t, source)
	s.repo = &exportPreflightRepo{err: errors.New("USAGE_EXPORT_QUEUE_FULL")}
	_, err := s.Create(context.Background(), UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}, UsageExportSpec{})
	require.Error(t, err)
	require.Zero(t, source.calls.Load())
}
func TestUsageExportConcurrentCreateDoesNotRepeatCount(t *testing.T) {
	source := &exportPreparingSource{entered: make(chan struct{}), release: make(chan struct{})}
	s := exportTestService(t, source)
	repo := &exportPreflightRepo{}
	s.repo = repo
	owner := UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}
	finished := make(chan error, 1)
	go func() {
		_, err := s.Create(context.Background(), owner, UsageExportSpec{SortBy: "id"})
		finished <- err
	}()
	<-source.entered
	_, err := s.Create(context.Background(), owner, UsageExportSpec{SortBy: "model"})
	require.Error(t, err)
	require.EqualValues(t, 1, source.calls.Load())
	require.EqualValues(t, 1, repo.calls.Load())
	close(source.release)
	require.NoError(t, <-finished)
}
func TestUsageExportWriterStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b bytes.Buffer
	w := usageExportLimitWriter{w: &b, max: 100, ctx: ctx}
	n, err := w.Write([]byte("never-written"))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	require.Empty(t, b.String())
}

type exportTicketRepo struct {
	UsageExportRepository
	job           *UsageExportJob
	inTransaction bool
	openCalls     int
}

func (r *exportTicketRepo) EndDownload(context.Context, string, string, string) error { return nil }
func (r *exportTicketRepo) RenewDownload(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func (r *exportTicketRepo) PeekByTicket(context.Context, string) (*UsageExportJob, error) {
	return r.job, nil
}
func (r *exportTicketRepo) GetByTicket(_ context.Context, _ string, open func(*UsageExportJob) error) (*UsageExportJob, error) {
	r.inTransaction = true
	defer func() { r.inTransaction = false }()
	r.openCalls++
	if err := open(r.job); err != nil {
		return nil, err
	}
	return r.job, nil
}

type exportTicketUsers struct {
	UserRepository
	t    *testing.T
	repo *exportTicketRepo
}

func (u exportTicketUsers) GetByID(context.Context, int64) (*User, error) {
	u.t.Helper()
	require.False(u.t, u.repo.inTransaction, "authentication must not acquire a second connection inside the file transaction")
	return &User{ID: 1, Role: "admin", Status: "active", TokenVersion: 2, TokenVersionResolved: true}, nil
}
func TestUsageExportDownloadAuthorizesBeforeFileTransaction(t *testing.T) {
	s := exportTestService(t, nil)
	repo := &exportTicketRepo{job: &UsageExportJob{ID: "job", FileKey: "job-claim.xlsx", Owner: UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "2"}}}
	s.repo = repo
	s.users = exportTicketUsers{t: t, repo: repo}
	require.NoError(t, os.WriteFile(s.path(repo.job.FileKey), []byte("export"), 0600))
	f, _, err := s.Download(context.Background(), strings.Repeat("a", 48), "")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, 1, repo.openCalls)
	repo.job.Owner.Revision = "1"
	_, _, err = s.Download(context.Background(), strings.Repeat("a", 48), "")
	require.ErrorIs(t, err, errUsageExportAccess)
	require.Equal(t, 1, repo.openCalls)
}
