package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/stretchr/testify/require"
)

func TestRuntimeMemoryUsesOpsSwitchAndProcessIdentity(t *testing.T) {
	svc := &OpsService{}
	svc.SetMonitoringEnabled(false)
	svc.startRuntimeMemory(nil, nil, nil)
	t.Cleanup(svc.StopRuntimeSettingsRefresh)
	require.Eventually(t, func() bool { return svc.RuntimeMemory() != nil }, time.Second, time.Millisecond)
	view := svc.GetRuntimeMemory(context.Background(), "")
	require.Equal(t, runtimeobs.InstanceID(), view.CurrentInstanceID)
	require.Empty(t, view.History)
	require.False(t, view.Profile.Enabled)
	require.False(t, svc.IsMonitoringEnabled(context.Background()))
	collector := NewOpsMetricsCollector(nil, nil, nil, nil, nil, nil, nil)
	require.Equal(t, runtimeobs.InstanceID(), collector.instanceID)
}

func TestRuntimeMemoryReportsSettlementOutcomes(t *testing.T) {
	before := runtimeobs.Default.SettlementSnapshot()
	runtimeobs.Default.ObserveSettlement(nil)
	runtimeobs.Default.ObserveSettlement(errors.New("fixture settlement failure"))
	svc := &OpsService{}
	svc.startRuntimeMemory(nil, nil, nil)
	t.Cleanup(svc.StopRuntimeSettingsRefresh)
	require.Eventually(t, func() bool {
		view := svc.GetRuntimeMemory(context.Background(), "")
		if len(view.History) == 0 {
			return false
		}
		queue := view.History[len(view.History)-1].UsageQueue
		return queue.SettlementSucceeded >= before.Succeeded+1 && queue.SettlementFailed >= before.Failed+1 && queue.Completed == 0
	}, time.Second, time.Millisecond)
}

func TestUsageRecordWorkerPoolNumericHistograms(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 2, TaskTimeout: time.Second})
	started := make(chan struct{})
	release := make(chan struct{})
	pool.Submit(func(context.Context) { close(started); <-release })
	<-started
	pool.Submit(func(context.Context) {})
	close(release)
	pool.Stop()
	stats := pool.Stats()
	require.Equal(t, 2, stats.QueueCapacity)
	var waits, executions uint64
	for _, n := range stats.WaitMS.Counts {
		waits += n
	}
	for _, n := range stats.ExecutionMS.Counts {
		executions += n
	}
	require.EqualValues(t, 2, waits)
	require.EqualValues(t, 2, executions)
	// These are scheduling outcomes; task closures do not report settlement success.
	require.EqualValues(t, 2, stats.CompletedTasks)
}
