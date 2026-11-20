package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

func ProvideOfficialStatusService(cfg *config.Config, store OfficialStatusStore, locks LeaderLockCache, settings *SettingService) *OfficialStatusService {
	client, err := httpclient.GetClient(httpclient.Options{Timeout: officialStatusTimeout, ProxyURL: cfg.Update.ProxyURL})
	if err != nil && (strings.TrimSpace(cfg.Update.ProxyURL) == "" || cfg.Security.ProxyFallback.AllowDirectOnError) {
		client, err = httpclient.GetClient(httpclient.Options{Timeout: officialStatusTimeout})
	}
	enabled := func(ctx context.Context) bool {
		return settings.GetChannelMonitorRuntime(ctx).PassiveAggregationAllowed()
	}
	if err != nil {
		return NewOfficialStatusService(publicCodexUnavailableClient{err: err}, store, locks, enabled)
	}
	clone := *client
	clone.Jar = nil
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return NewOfficialStatusService(&clone, store, locks, enabled)
}
