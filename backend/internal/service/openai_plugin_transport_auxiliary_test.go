package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type codexAuxiliaryTransportUpstream struct {
	pluginRoutingHTTPUpstream
	request     *http.Request
	proxyURL    string
	accountID   int64
	concurrency int
}

func (u *codexAuxiliaryTransportUpstream) Do(request *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.request = request
	u.proxyURL = proxyURL
	u.accountID = accountID
	u.concurrency = concurrency
	return u.pluginRoutingHTTPUpstream.Do(request, proxyURL, accountID, concurrency)
}

func TestCodexAuxiliaryUpstreamUsesIndependentUnlimitedTransport(t *testing.T) {
	for _, withManager := range []bool{false, true} {
		name := "without_plugin_manager"
		if withManager {
			name = "without_enabled_plugin_binding"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &codexAuxiliaryTransportUpstream{}
			service := &OpenAIGatewayService{httpUpstream: upstream}
			if withManager {
				service.pluginManager = &PluginManager{}
			}
			request, err := http.NewRequestWithContext(
				WithHTTPUpstreamProfile(context.Background(), HTTPUpstreamProfileOpenAI),
				http.MethodPost, "https://example.com/alpha/history/v2/search_contents", nil,
			)
			require.NoError(t, err)
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 7}

			response, err := service.doCodexAuxiliaryUpstream(request, "http://proxy.example:8080", account)

			require.NoError(t, err)
			require.NotNil(t, response)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, 1, upstream.doCalls)
			assert.Equal(t, "http://proxy.example:8080", upstream.proxyURL)
			assert.Equal(t, account.ID, upstream.accountID)
			assert.Zero(t, upstream.concurrency)
			assert.Equal(t, HTTPUpstreamProfileCodexAuxiliary, HTTPUpstreamProfileFromContext(upstream.request.Context()))
			assert.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(request.Context()))
			assert.Equal(t, 7, account.Concurrency)
		})
	}
}

type codexAuxiliaryPluginClient struct {
	pluginv1.TransportPluginClient
	profile HTTPUpstreamProfile
	start   *pluginv1.ForwardRequestStart
}

func (c *codexAuxiliaryPluginClient) Forward(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse], error) {
	c.profile = HTTPUpstreamProfileFromContext(ctx)
	return &codexAuxiliaryPluginStream{client: c}, nil
}

type codexAuxiliaryPluginStream struct {
	grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse]
	client *codexAuxiliaryPluginClient
}

func (s *codexAuxiliaryPluginStream) Send(request *pluginv1.ForwardRequest) error {
	s.client.start = request.GetStart()
	return errors.New("stop after capturing plugin request metadata")
}

func TestCodexAuxiliaryUpstreamPreservesPluginRouteWithoutAccountConcurrency(t *testing.T) {
	api := &codexAuxiliaryPluginClient{}
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{
		pluginID: 1, rolloutPercent: 100,
		runtime: &pluginRuntime{client: hcplugin.NewClient(&hcplugin.ClientConfig{}), api: api},
	})
	upstream := &codexAuxiliaryTransportUpstream{}
	service := &OpenAIGatewayService{pluginManager: manager, httpUpstream: upstream}
	request, err := http.NewRequest(http.MethodPost, "https://example.com/alpha/notes/v2/thread_hint", nil)
	require.NoError(t, err)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 7}

	response, err := service.doCodexAuxiliaryUpstream(request, "http://proxy.example:8080", account)

	require.ErrorContains(t, err, "stop after capturing plugin request metadata")
	assert.Nil(t, response)
	assert.Zero(t, upstream.doCalls, "a selected plugin must not fall back after a transport error")
	require.NotNil(t, api.start)
	assert.Equal(t, account.ID, api.start.AccountId)
	assert.Equal(t, "http://proxy.example:8080", api.start.ProxyUrl)
	assert.Zero(t, api.start.AccountConcurrency)
	assert.Equal(t, HTTPUpstreamProfileCodexAuxiliary, api.profile)
	assert.Equal(t, 7, account.Concurrency, "other requests must retain the real account concurrency")
}

func TestCodexAuxiliaryUpstreamFailsClosedForUnavailablePlugin(t *testing.T) {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "test unavailable"})
	upstream := &codexAuxiliaryTransportUpstream{}
	service := &OpenAIGatewayService{pluginManager: manager, httpUpstream: upstream}
	request, err := http.NewRequest(http.MethodPost, "https://example.com/alpha/history/v2/search_contents", nil)
	require.NoError(t, err)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 7}

	response, err := service.doCodexAuxiliaryUpstream(request, "", account)

	require.ErrorContains(t, err, "插件不可用")
	assert.Nil(t, response)
	assert.Zero(t, upstream.doCalls)
	assert.Equal(t, 7, account.Concurrency)
}
