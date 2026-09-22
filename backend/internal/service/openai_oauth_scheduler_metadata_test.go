//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Unlike the general scheduler stub, this preserves the real candidate/full
// account split: candidates never contain tokens or synthesize authorization.
type oauthMetadataSnapshotCache struct {
	openAISnapshotCacheStub
	candidates   []*Account
	hydrateCalls int
}

func (c *oauthMetadataSnapshotCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.candidates, true, nil
}

func (c *oauthMetadataSnapshotCache) GetAccount(_ context.Context, id int64) (*Account, error) {
	c.hydrateCalls++
	return c.accountsByID[id], nil
}

func TestOpenAIOAuthSchedulerMetadataHydratesUsableAccount(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, available := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%s/credentials=%t", advanced, available), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
				account := schedulerOSAuthorizedAccount(1652, OpenAIOSWindows, OpenAIOSWindows)
				if !available {
					account.Credentials = nil
				}
				metadata := account
				metadata.Credentials = nil
				metadata.OpenAIOAuthCredentialsAvailable = &available
				cache := &oauthMetadataSnapshotCache{
					openAISnapshotCacheStub: openAISnapshotCacheStub{accountsByID: map[int64]*Account{account.ID: &account}},
					candidates:              []*Account{&metadata},
				}
				repo := newSchedulerOSAuthorizationTestRepo(account)
				svc := &OpenAIGatewayService{
					accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{cache: cache},
					cfg: newSchedulerTestOpenAIWSV2Config(), cache: &schedulerTestGatewayCache{},
					rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
					concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				}
				ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
				selected, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
				if !available {
					require.ErrorIs(t, err, ErrNoAvailableOpenAIOAuthOSAccounts)
					require.Nil(t, selected)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, account.ID, selected.Account.ID)
				require.Equal(t, "access-1652", selected.Account.GetOpenAIAccessToken())
				require.Equal(t, OpenAIOSLinux, selected.Account.OpenAIOAuthCredentialOS)
				require.Nil(t, selected.Account.OpenAIOAuthCredentialsAvailable, "full account uses actual credentials")
				require.Positive(t, cache.hydrateCalls)
				require.Empty(t, metadata.Credentials, "selection must not fill tokens into candidate metadata")
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}

				// A stale positive summary is not authority to send after revocation.
				repo.accounts[0].Credentials = nil
				selected, _, err = svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
				require.Error(t, err)
				require.Nil(t, selected)
			})
		}
	}
}

func TestOpenAIOAuthSchedulerMetadataAvailabilityUsesOwnerAcrossSystems(t *testing.T) {
	parent := schedulerOSAuthorizedAccount(1371, OpenAIOSWindows, OpenAIOSWindows)
	parent.Credentials = nil
	shadow := Account{ID: 1372, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID}
	for _, available := range []bool{false, true} {
		parent.OpenAIOAuthCredentialsAvailable = &available
		for _, os := range []string{"", OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux} {
			ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: os})
			require.Equal(t, available, openAIAccountOSAuthorizationEligible(ctx, &shadow, func(id int64) *Account {
				require.Equal(t, parent.ID, id)
				return &parent
			}))
		}
	}
	parent.OpenAIOAuthCredentialsAvailable = nil
	require.False(t, OpenAIOAuthOSAuthorizationAvailable(&parent, ""), "old OS summaries cannot substitute for account credentials")
}
