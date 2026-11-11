package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

type publicCodexUnavailableClient struct{ err error }

func (c publicCodexUnavailableClient) Do(*http.Request) (*http.Response, error) { return nil, c.err }

func ProvidePublicCodexCatalogService(cfg *config.Config, store PublicCodexCatalogStore) *PublicCodexCatalogService {
	var proxy string
	var allowDirect bool
	if cfg != nil {
		proxy = cfg.Update.ProxyURL
		allowDirect = cfg.Security.ProxyFallback.AllowDirectOnError
	}
	client, err := httpclient.GetClient(httpclient.Options{Timeout: publicCodexCatalogTimeout, ProxyURL: proxy})
	if err != nil && (strings.TrimSpace(proxy) == "" || allowDirect) {
		client, err = httpclient.GetClient(httpclient.Options{Timeout: publicCodexCatalogTimeout})
	}
	var transport publicCodexHTTPClient
	if err != nil {
		transport = publicCodexUnavailableClient{err: fmt.Errorf("public catalog proxy initialization failed: %w", err)}
	} else {
		// Never borrow a credentialed client or follow a redirect to another source.
		cloned := *client
		cloned.Jar = nil
		cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		transport = &cloned
	}
	svc := NewPublicCodexCatalogService(transport, store)
	svc.Start()
	return svc
}
