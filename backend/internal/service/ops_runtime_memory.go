package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/redis/go-redis/v9"
)

func (s *OpsService) startRuntimeMemory(client *redis.Client, pool *UsageRecordWorkerPool, schedulerCache SchedulerCache) {
	var providers []runtimeobs.CacheStatsProvider
	if provider, ok := schedulerCache.(runtimeobs.CacheStatsProvider); ok {
		providers = append(providers, provider)
	}
	var store runtimeobs.Store
	if client != nil {
		store = runtimeobs.NewRedisStore(client)
	}
	s.runtimeMemory = runtimeobs.NewCollector(store, func() bool { return s.IsMonitoringEnabled(context.Background()) }, func() runtimeobs.QueueStats {
		v := pool.Stats()
		settlement := runtimeobs.Default.SettlementSnapshot()
		return runtimeobs.QueueStats{
			Capacity: v.QueueCapacity, Waiting: v.WaitingTasks, Running: v.RunningWorkers,
			Submitted: v.SubmittedTasks, Completed: v.CompletedTasks, Failed: v.FailedTasks,
			Dropped: v.DroppedQueueFull + v.DroppedPoolStopped, SyncFallback: v.SyncFallbackTasks,
			SettlementSucceeded: settlement.Succeeded, SettlementFailed: settlement.Failed,
			WaitMS: v.WaitMS, ExecutionMS: v.ExecutionMS,
		}
	}, providers...)
	s.runtimeMemory.Start()
}

func (s *OpsService) RuntimeMemory() *runtimeobs.Collector {
	if s == nil {
		return nil
	}
	return s.runtimeMemory
}
func (s *OpsService) GetRuntimeMemory(ctx context.Context, instance string) runtimeobs.View {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.runtimeMemory.View(ctx, instance)
}
