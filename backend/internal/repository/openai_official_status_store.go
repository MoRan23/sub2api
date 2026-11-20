package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const officialStatusSnapshotKey = "openai:official-status:snapshot:v1"

type officialStatusStore struct{ client *redis.Client }

func NewOfficialStatusStore(client *redis.Client) service.OfficialStatusStore {
	return &officialStatusStore{client: client}
}

func (s *officialStatusStore) Load(ctx context.Context) (*service.OfficialStatusSnapshot, error) {
	data, err := s.client.Get(ctx, officialStatusSnapshotKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Invalid cached data is discarded so the next lease holder can repair it.
	if len(data) > 1<<20 {
		return nil, nil
	}
	var snapshot service.OfficialStatusSnapshot
	if json.Unmarshal(data, &snapshot) != nil || service.ValidateOfficialStatusSnapshot(&snapshot) != nil {
		return nil, nil
	}
	return &snapshot, nil
}

func (s *officialStatusStore) Save(ctx context.Context, snapshot *service.OfficialStatusSnapshot) error {
	if err := service.ValidateOfficialStatusSnapshot(snapshot); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("official status cache exceeds limit")
	}
	// The refresh lease exceeds the bounded network deadline; only its holder writes.
	return s.client.Set(ctx, officialStatusSnapshotKey, data, 0).Err()
}
