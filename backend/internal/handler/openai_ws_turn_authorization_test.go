package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type wsTurnAuthorizationStaticRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

// This fixture never reconciles receipts: it only verifies that an authorized
// Engine turn reaches the physical transport with its required dispatch record.
type wsTurnAuthorizationEngineStore struct {
	service.CodexEngineBillingStore
	nextID atomic.Int64
}

func (s *wsTurnAuthorizationEngineStore) CreatePending(_ context.Context, pending *service.CodexEngineBillingPending) (*service.CodexEngineBillingJob, error) {
	return &service.CodexEngineBillingJob{ID: s.nextID.Add(1), CodexEngineBillingPending: *pending}, nil
}

func (*wsTurnAuthorizationEngineStore) ClaimDue(context.Context, int, time.Duration) ([]*service.CodexEngineBillingJob, error) {
	return nil, nil
}

func (*wsTurnAuthorizationEngineStore) FinalizeDelivery(context.Context, int64, string) error {
	return nil
}

func (r *wsTurnAuthorizationStaticRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if r.key == nil || key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	cloned := *r.key
	return &cloned, nil
}

func TestOpenAIWSTurnAuthorizationRejectsStaleIdentity(t *testing.T) {
	repo := newWSGroupPricingAPIKeyRepoStub(3)
	conn, err := repo.GetByKeyForAuth(context.Background(), wsGroupPricingKey)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		change    func(*service.APIKey)
		lookupErr error
	}{
		{name: "disabled user", change: func(k *service.APIKey) { k.User.Status = service.StatusDisabled }},
		{name: "disabled key", change: func(k *service.APIKey) { k.Status = service.StatusAPIKeyDisabled }},
		{name: "expired key", change: func(k *service.APIKey) { at := time.Now().Add(-time.Second); k.ExpiresAt = &at }},
		{name: "different key", change: func(k *service.APIKey) { k.ID++ }},
		{name: "different owner", change: func(k *service.APIKey) { k.UserID++; k.User.ID++ }},
		{name: "missing user", change: func(k *service.APIKey) { k.User = nil }},
		{name: "missing key", lookupErr: service.ErrAPIKeyNotFound},
		{name: "lookup failure", lookupErr: errors.New("auth cache unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latest := *conn
			user := *conn.User
			latest.User = &user
			if tc.change != nil {
				tc.change(&latest)
			}
			var keys openAIWSTurnBillingAPIKeys
			keys.admittedTurn, keys.admittedKey = 2, conn
			err := keys.authorizeNextTurn(context.Background(), wsTurnAPIKeyLookupFunc(func(context.Context, string) (*service.APIKey, error) {
				return &latest, tc.lookupErr
			}), 2, conn)
			require.Error(t, err)
			var closeErr *service.OpenAIWSClientCloseError
			require.ErrorAs(t, err, &closeErr)
			want := coderws.StatusPolicyViolation
			if tc.name == "lookup failure" {
				want = coderws.StatusTryAgainLater
			}
			require.Equal(t, want, closeErr.StatusCode())
			require.Nil(t, keys.takeAdmittedKey(2), "failed revalidation must discard a prior admitted snapshot")
			require.Equal(t, service.StatusActive, conn.User.Status, "never mutate the handshake snapshot")
		})
	}
}

func TestOpenAIWSTurnAuthorizationSharesLookupWithoutChangingBillingIdentity(t *testing.T) {
	repo := newWSGroupPricingAPIKeyRepoStub(3)
	svc := newWSGroupPricingAPIKeyService(repo)
	conn, err := svc.GetByKey(context.Background(), wsGroupPricingKey)
	require.NoError(t, err)
	var keys openAIWSTurnBillingAPIKeys
	require.NoError(t, keys.authorizeNextTurn(context.Background(), nil, 1, conn))
	require.NoError(t, repo.adminUpdate(svc, func(k *service.APIKey, group *service.Group) {
		group.RateMultiplier = 0.3
		k.User = &service.User{ID: k.UserID, Status: service.StatusActive, Balance: 999}
	}))
	require.NoError(t, keys.authorizeNextTurn(context.Background(), svc, 2, conn))
	lookups := repo.lookupCount()
	keys.begin(context.Background(), svc, 2, conn)
	require.Equal(t, lookups, repo.lookupCount(), "billing reuses the admission lookup")
	got := keys.forTurn(2, conn)
	require.InDelta(t, 0.3, got.Group.RateMultiplier, 1e-12)
	require.Same(t, conn.User, got.User, "only the eligible billing group is replaced")
	require.Nil(t, keys.takeAdmittedKey(2), "admission snapshot is released after billing captures its group")
}

func TestOpenAIWSTurnAuthorizationMissingServiceFailsClosed(t *testing.T) {
	repo := newWSGroupPricingAPIKeyRepoStub(3)
	conn, err := repo.GetByKeyForAuth(context.Background(), wsGroupPricingKey)
	require.NoError(t, err)
	var svc *service.APIKeyService
	var keys openAIWSTurnBillingAPIKeys
	require.Error(t, keys.authorizeNextTurn(context.Background(), svc, 2, conn))
	require.Nil(t, keys.takeAdmittedKey(2))
}

func TestOpenAIResponsesWebSocket_NextTurnRechecksAuthorization(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, "engine"} {
		for _, scenario := range []string{"active", "user disabled", "key disabled", "lookup failure"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				repo := newWSGroupPricingAPIKeyRepoStub(3)
				svc := newWSGroupPricingAPIKeyService(repo)
				tc := openAIResponsesWSUsageLogCase{
					firstPayload: wsGroupPricingFirst, secondPayload: wsGroupPricingSecond,
					apiKeyService: svc, apiKeyCredential: wsGroupPricingKey,
					codexEngine: mode == "engine", assertTurnSlotsReleased: true,
					secondTurnCloseExpected: scenario != "active",
					closeStatus:             coderws.StatusPolicyViolation,
				}
				if mode != "engine" {
					tc.ingressMode = mode
				}
				switch scenario {
				case "user disabled":
					tc.closeReason = "User account is not active"
				case "key disabled":
					tc.closeReason = "API key is no longer active"
				case "lookup failure":
					tc.closeStatus, tc.closeReason = coderws.StatusTryAgainLater, "unable to verify websocket authorization"
				}
				tc.afterFirstUpstreamRequest = func(*service.ChannelService) error {
					return repo.adminUpdate(svc, func(k *service.APIKey, _ *service.Group) {
						switch scenario {
						case "user disabled":
							k.User = &service.User{ID: k.UserID, Status: service.StatusDisabled}
						case "key disabled":
							k.Status = service.StatusAPIKeyDisabled
						case "lookup failure":
							repo.err = errors.New("auth store unavailable")
						}
					})
				}
				got := runOpenAIResponsesWebSocketUsageLogCase(t, tc)
				if scenario == "active" {
					require.Len(t, got.upstreamPayloads, 2)
				}
				require.Equal(t, 2, repo.lookupCount(), "one handshake and one next-turn lookup; billing must not read again")
			})
		}
	}
}
