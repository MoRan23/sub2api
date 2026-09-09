package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	for _, dependency := range codexPATSettingsKeys {
		if key == dependency {
			return r.setMultipleWithCodexPATValidation(ctx, map[string]string{key: value})
		}
	}
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	for _, key := range codexPATSettingsKeys {
		if _, changesDependency := settings[key]; changesDependency {
			return r.setMultipleWithCodexPATValidation(ctx, settings)
		}
	}
	return setMultipleSettings(ctx, r.client, settings)
}

var codexPATSettingsKeys = []string{
	service.SettingKeyEnableOpenAICodexPATContextManagement,
	service.SettingKeyEnableOpenAICodexFingerprintNormalization,
	service.SettingKeyEnableOpenAIUUIDv7SessionIdentity,
}

// The transaction-scoped lock serializes the three dependent settings even
// when a row is absent and writers run in different application instances.
const codexPATSettingsLockID int64 = 0x434f444558504154

func (r *settingRepository) setMultipleWithCodexPATValidation(ctx context.Context, updates map[string]string) error {
	// A fresh statement snapshot after obtaining the lock must see the previous
	// writer's commit, including when the database default isolation is stronger.
	tx, err := r.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", codexPATSettingsLockID); err != nil {
		return fmt.Errorf("lock Codex PAT settings: %w", err)
	}
	current, err := (&settingRepository{client: client}).GetMultiple(ctx, codexPATSettingsKeys)
	if err != nil {
		return err
	}
	for key, value := range updates {
		current[key] = value
	}
	if err := service.ValidateOpenAICodexPATContextManagementValues(current); err != nil {
		return err
	}
	if err := setMultipleSettings(ctx, client, updates); err != nil {
		return err
	}
	return tx.Commit()
}

func setMultipleSettings(ctx context.Context, client *ent.Client, settings map[string]string) error {
	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}
