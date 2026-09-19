//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func codexStateNamedPubSubClientID(ctx context.Context, name string) (string, error) {
	list, err := integrationRedis.ClientList(ctx).Result()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(list, "\n") {
		fields := make(map[string]string)
		for _, field := range strings.Fields(line) {
			if key, value, ok := strings.Cut(field, "="); ok {
				fields[key] = value
			}
		}
		if fields["name"] == name && strings.Contains(fields["flags"], "P") {
			return fields["id"], nil
		}
	}
	return "", nil
}

// These subscriptions use distinct physical clients on the real Redis test
// container. CLIENT KILL targets only the uniquely named subscriber connection.
func TestCodexStateRedisIntegrationNotificationsPayloadReconnectAndCleanup(t *testing.T) {
	for _, kind := range []string{"activation", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			publisherOptions, subscriberOptions := *integrationRedis.Options(), *integrationRedis.Options()
			subscriberName := "codex-state-integration-" + uuid.NewString()
			subscriberOptions.ClientName = subscriberName
			publisherClient, subscriberClient := redis.NewClient(&publisherOptions), redis.NewClient(&subscriberOptions)
			t.Cleanup(func() { _ = publisherClient.Close(); _ = subscriberClient.Close() })
			publisher := &openAICodexStateRepository{rdb: publisherClient}
			subscriber := &openAICodexStateRepository{rdb: subscriberClient}
			key := service.CodexTurnStateKey{OwnerAccountID: 8675309, Model: "gpt-5.4", Generation: "synthetic-generation"}
			channel := codexStateActivationChannel
			expected := map[string]any{"owner_account_id": float64(key.OwnerAccountID), "generation": key.Generation}
			publish := func() error { return publisher.PublishActivation(ctx, key.OwnerAccountID, key.Generation) }
			subscribe := func(handle func(string)) error {
				return subscriber.SubscribeActivations(ctx, func(id int64, generation string) {
					handle(fmt.Sprintf("%d|%s", id, generation))
				})
			}
			wantedCallback := fmt.Sprintf("%d|%s", key.OwnerAccountID, key.Generation)
			invalid := []string{`not-json`, `{}`, `{"owner_account_id":0,"generation":"generation"}`, `{"owner_account_id":17,"generation":" "}`}
			if kind == "cancellation" {
				channel = codexStateCancelChannel
				expected = map[string]any{"OwnerAccountID": float64(key.OwnerAccountID), "Model": key.Model, "Generation": key.Generation}
				publish = func() error { return publisher.PublishCancel(ctx, key) }
				subscribe = func(handle func(string)) error {
					return subscriber.SubscribeCancels(ctx, func(actual service.CodexTurnStateKey) {
						handle(fmt.Sprintf("%d|%s|%s", actual.OwnerAccountID, actual.Generation, actual.Model))
					})
				}
				wantedCallback += "|" + key.Model
				invalid = []string{`not-json`, `{}`, `{"OwnerAccountID":0,"Model":"gpt-5.4","Generation":"generation"}`, `{"OwnerAccountID":17,"Model":"gpt-5.4"}`}
			}
			baseline, err := integrationRedis.PubSubNumSub(ctx, channel).Result()
			require.NoError(t, err)
			require.Zero(t, baseline[channel], "the isolated test must start without subscribers")
			callbacks, done := make(chan string, 8), make(chan error, 1)
			go func() { done <- subscribe(func(value string) { callbacks <- value }) }()
			stopped := false
			t.Cleanup(func() {
				cancel()
				if !stopped {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("subscription did not stop after test cleanup")
					}
				}
			})
			var oldClientID string
			require.Eventually(t, func() bool {
				oldClientID, err = codexStateNamedPubSubClientID(ctx, subscriberName)
				return err == nil && oldClientID != ""
			}, time.Second, time.Millisecond)
			for _, raw := range append(invalid, strings.Repeat("x", 4097)) {
				require.NoError(t, publisherClient.Publish(ctx, channel, raw).Err())
			}
			wire := publisherClient.Subscribe(ctx, channel)
			defer wire.Close()
			_, err = wire.Receive(ctx)
			require.NoError(t, err)
			require.NoError(t, publish())
			message, err := wire.ReceiveMessage(ctx)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(message.Payload), &payload))
			require.Equal(t, expected, payload, "the wire message must contain only the account/config/model scope")
			select {
			case actual := <-callbacks:
				require.Equal(t, wantedCallback, actual, "invalid messages must not reach the callback")
			case <-ctx.Done():
				t.Fatal("real Redis did not deliver the notification")
			}
			require.NoError(t, wire.Close())
			killed, err := integrationRedis.Do(ctx, "CLIENT", "KILL", "ID", oldClientID).Int64()
			require.NoError(t, err)
			require.EqualValues(t, 1, killed)
			require.Eventually(t, func() bool {
				newClientID, clientErr := codexStateNamedPubSubClientID(ctx, subscriberName)
				return clientErr == nil && newClientID != "" && newClientID != oldClientID
			}, 3*time.Second, 5*time.Millisecond, "the real Redis subscription must reconnect after losing its socket")
			require.NoError(t, publish())
			select {
			case actual := <-callbacks:
				require.Equal(t, wantedCallback, actual)
			case <-ctx.Done():
				t.Fatal("the reconnected subscription did not receive the next notification")
			}
			cancel()
			select {
			case err := <-done:
				stopped = true
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("idle subscription did not honor context cancellation")
			}
			require.Eventually(t, func() bool {
				counts, countErr := integrationRedis.PubSubNumSub(context.Background(), channel).Result()
				return countErr == nil && counts[channel] == 0
			}, time.Second, time.Millisecond, "canceling the subscription must release the Redis channel")
		})
	}
}
