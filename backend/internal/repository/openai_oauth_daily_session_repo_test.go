package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

// Keep the repository contract checked at compile time as the pool evolves.
var _ service.OAuthDailySessionRepository = (*openAIOAuthDailySessionRepository)(nil)
