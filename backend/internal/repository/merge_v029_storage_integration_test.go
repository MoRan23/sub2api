//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMergeV029ChannelImagePricesPreserveNilAndZero(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelRepository(integrationDB)
	zero, price := 0.0, 0.25
	channel := &service.Channel{
		Name: uniqueTestValue(t, "v029-image-pricing"), Status: service.StatusActive,
		BillingModelSource: service.BillingModelSourceChannelMapped,
		ModelPricing: []service.ChannelModelPricing{
			{Platform: service.PlatformOpenAI, Models: []string{"inherit"}, BillingMode: service.BillingModeToken},
			{Platform: service.PlatformOpenAI, Models: []string{"free"}, BillingMode: service.BillingModeToken, ImageInputPrice: &zero, ImageOutputPrice: &zero},
			{Platform: service.PlatformOpenAI, Models: []string{"priced"}, BillingMode: service.BillingModeToken, ImageInputPrice: &price, ImageOutputPrice: &price},
		},
	}
	require.NoError(t, repo.Create(ctx, channel))
	t.Cleanup(func() { _ = repo.Delete(context.Background(), channel.ID) })
	assertPricing := func() *service.Channel {
		stored, err := repo.GetByID(ctx, channel.ID)
		require.NoError(t, err)
		require.Len(t, stored.ModelPricing, 3)
		for _, item := range stored.ModelPricing {
			switch item.Models[0] {
			case "inherit":
				require.Nil(t, item.ImageInputPrice)
				require.Nil(t, item.ImageOutputPrice)
			case "free":
				require.NotNil(t, item.ImageInputPrice)
				require.NotNil(t, item.ImageOutputPrice)
				require.Zero(t, *item.ImageInputPrice)
				require.Zero(t, *item.ImageOutputPrice)
			case "priced":
				require.NotNil(t, item.ImageInputPrice)
				require.NotNil(t, item.ImageOutputPrice)
				require.Equal(t, price, *item.ImageInputPrice)
				require.Equal(t, price, *item.ImageOutputPrice)
			default:
				t.Fatalf("unexpected pricing model %q", item.Models)
			}
		}
		return stored
	}
	stored := assertPricing()
	stored.Description = "ordinary edit must retain inheritance and explicit free prices"
	require.NoError(t, repo.Update(ctx, stored))
	assertPricing()
}

func TestMergeV029AccountLongContextFlagReachesScheduler(t *testing.T) {
	ctx := context.Background()
	repo, account := newQuotaPreflightFixture(t)
	cache := NewSchedulerCache(testRedis(t))
	repo.schedulerCache = cache
	for _, enabled := range []bool{false, true, false} {
		require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{"openai_long_context_billing_enabled": enabled}))
		stored, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, enabled, stored.Extra["openai_long_context_billing_enabled"])
		cached, err := cache.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.NotNil(t, cached)
		require.Equal(t, enabled, cached.Extra["openai_long_context_billing_enabled"])
	}
}

func TestMergeV029IdempotencyCoordinatorsSerializeSyntheticReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config := service.DefaultIdempotencyConfig()
	config.ObserveOnly = false
	first := service.NewIdempotencyCoordinator(NewIdempotencyRepository(testEntClient(t), integrationDB), config)
	second := service.NewIdempotencyCoordinator(NewIdempotencyRepository(testEntClient(t), integrationDB), config)
	options := service.IdempotencyExecuteOptions{
		Scope: uniqueTestValue(t, "quota-reset-cycle"), ActorScope: "system",
		Method: "POST", Route: "/synthetic/reset", IdempotencyKey: "one-credit-one-cycle", RequireKey: true,
		Payload: map[string]any{"credit": "synthetic-credit"}, TTL: time.Hour,
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope=$1", options.Scope)
	})
	var sends atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		_, err := first.Execute(ctx, options, func(context.Context) (any, error) {
			sends.Add(1)
			close(entered)
			<-release
			return map[string]any{"code": "ok", "windows_reset": 1}, nil
		})
		ownerDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("synthetic reset owner did not start")
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := second.Execute(ctx, options, func(context.Context) (any, error) {
				sends.Add(1)
				return nil, nil
			})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, service.ErrIdempotencyInProgress)
	}
	close(release)
	require.NoError(t, <-ownerDone)
	replayed, err := second.Execute(ctx, options, func(context.Context) (any, error) {
		sends.Add(1)
		return nil, nil
	})
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.EqualValues(t, 1, sends.Load(), "two coordinators must perform one synthetic reset across contention and retry")
}
