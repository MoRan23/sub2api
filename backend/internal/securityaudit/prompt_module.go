package securityaudit

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/wire"
)

func ProvidePromptService(config ConfigStore, repo *PostgreSQLRepository, payload *RedisPayloadStore, scanner *OpenAICompatibleScanner, metrics *AtomicMetrics, invalidator *service.APIKeyService, email *service.NotificationEmailService) *PromptService {
	s := NewPromptService(config, repo, payload, scanner, metrics)
	s.SetEnforcementDependencies(repo, invalidator)
	s.notifications = newPromptNotificationWorker(repo, email)
	return s
}

var ProviderSet = wire.NewSet(
	NewPostgreSQLRepository,
	wire.Bind(new(JobRepository), new(*PostgreSQLRepository)),
	wire.Bind(new(EventRepository), new(*PostgreSQLRepository)),
	NewRedisPayloadStore,
	wire.Bind(new(PayloadStore), new(*RedisPayloadStore)),
	NewOpenAICompatibleScanner,
	wire.Bind(new(PromptScanner), new(*OpenAICompatibleScanner)),
	NewAtomicMetrics,
	wire.Bind(new(Metrics), new(*AtomicMetrics)),
	NewConfigManager,
	wire.Bind(new(ConfigStore), new(*ConfigManager)),
	ProvidePromptService,
	wire.Bind(new(PromptEngine), new(*PromptService)),
	wire.Bind(new(PromptAdminService), new(*PromptService)),
	NewLegacyModerationAdapter,
	NewCoordinator,
	NewPromptAdminHandler,
)
