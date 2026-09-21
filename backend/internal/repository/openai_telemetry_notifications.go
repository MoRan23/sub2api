package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const codexTelemetryWakeChannel = "codex_telemetry:wake:v1"

type codexTelemetryNotifier struct {
	rdb *redis.Client
}

func NewCodexTelemetryNotifier(rdb *redis.Client) service.CodexTelemetryNotifier {
	return &codexTelemetryNotifier{rdb: rdb}
}

func (n *codexTelemetryNotifier) Notify(ctx context.Context) error {
	if n == nil || n.rdb == nil {
		return errors.New("Codex telemetry notification Redis unavailable")
	}
	// Bound socket I/O too: the shared Redis client may disable context timeouts.
	return n.rdb.WithTimeout(time.Second).Publish(ctx, codexTelemetryWakeChannel, "wake").Err()
}

func (n *codexTelemetryNotifier) Subscribe(ctx context.Context, handle func()) error {
	if n == nil || n.rdb == nil {
		return errors.New("Codex telemetry notification Redis unavailable")
	}
	if handle == nil {
		return errors.New("nil Codex telemetry notification handler")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pubsub := n.rdb.Subscribe(ctx, codexTelemetryWakeChannel)
	defer func() { _ = pubsub.Close() }()
	// Cancellation must also interrupt the initial subscription handshake.
	stopClose := context.AfterFunc(ctx, func() { _ = pubsub.Close() })
	defer stopClose()
	if _, err := pubsub.ReceiveTimeout(ctx, time.Second); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-messages:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("Codex telemetry notification subscription closed")
			}
			// Nothing from Redis is applied as policy or included in diagnostics.
			if message.Payload == "wake" && ctx.Err() == nil {
				handle()
			}
		}
	}
}
