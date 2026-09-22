//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Preserve the candidate/full account split: scheduling metadata has no tokens.
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

func TestOpenAIOAuthSchedulerMetadataHydratesWithoutCredentialAdmission(t *testing.T) {
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
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, account.ID, selected.Account.ID)
				require.Equal(t, account.GetOpenAIAccessToken(), selected.Account.GetOpenAIAccessToken())
				require.Empty(t, selected.Account.OpenAIOAuthCredentialOS, "scheduling leaves identity selection to request forwarding")
				require.Zero(t, repo.credentialReads.Load())
				require.Positive(t, cache.hydrateCalls)
				require.Empty(t, metadata.Credentials, "selection must not fill tokens into candidate metadata")
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
			})
		}
	}
}
