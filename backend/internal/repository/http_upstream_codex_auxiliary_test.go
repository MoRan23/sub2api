package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexAuxiliaryHTTP1RequestsBypassSaturatedModelPool(t *testing.T) {
	for _, isolation := range []string{config.ConnectionPoolIsolationAccount, config.ConnectionPoolIsolationAccountProxy, config.ConnectionPoolIsolationProxy} {
		t.Run(isolation, func(t *testing.T) {
			modelRelease, auxiliaryRelease := make(chan struct{}), make(chan struct{})
			arrivals := make(chan struct{}, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/model" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					select {
					case <-modelRelease:
					case <-r.Context().Done():
					}
					return
				}
				arrivals <- struct{}{}
				select {
				case <-auxiliaryRelease:
					_, _ = io.WriteString(w, `{"items":[]}`)
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(server.Close)
			var releaseAuxiliary sync.Once
			t.Cleanup(func() { close(modelRelease); releaseAuxiliary.Do(func() { close(auxiliaryRelease) }) })
			upstream := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{
				ConnectionPoolIsolation: isolation,
				MaxConnsPerHost:         1,
				OpenAIHTTP2:             config.GatewayOpenAIHTTP2Config{Enabled: false},
			}}).(*httpUpstreamService)
			modelProfile := service.HTTPUpstreamProfileOpenAI
			auxiliaryProfile := service.HTTPUpstreamProfileCodexAuxiliary
			modelReq, err := http.NewRequestWithContext(service.WithHTTPUpstreamProfile(t.Context(), modelProfile), http.MethodGet, server.URL+"/model", nil)
			require.NoError(t, err)
			modelResp, err := upstream.Do(modelReq, "", 71, 1)
			require.NoError(t, err)
			t.Cleanup(func() { _ = modelResp.Body.Close() })
			require.Equal(t, 1, modelResp.ProtoMajor)
			modelEntry, err := upstream.getClientEntry("", 71, 1, modelProfile, false, false)
			require.NoError(t, err)
			require.Equal(t, int64(1), atomic.LoadInt64(&modelEntry.inFlight))

			// The open streaming response still occupies the model's sole connection.
			blockedCtx, cancelBlocked := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancelBlocked()
			blockedReq := modelReq.Clone(service.WithHTTPUpstreamProfile(blockedCtx, modelProfile))
			blockedResp, err := upstream.Do(blockedReq, "", 71, 1)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Nil(t, blockedResp)

			results := make(chan error, 2)
			for range 2 {
				go func() {
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					defer cancel()
					req, err := http.NewRequestWithContext(service.WithHTTPUpstreamProfile(ctx, auxiliaryProfile), http.MethodGet, server.URL+"/auxiliary", nil)
					if err != nil {
						results <- err
						return
					}
					// Even a caller that supplies the original account concurrency must
					// receive the unlimited, independent auxiliary transport.
					resp, err := upstream.Do(req, "", 71, 1)
					if err == nil {
						_, readErr := io.ReadAll(resp.Body)
						err = errors.Join(readErr, resp.Body.Close())
					}
					results <- err
				}()
			}
			for range 2 {
				select {
				case <-arrivals:
				case <-time.After(2 * time.Second):
					t.Fatal("both auxiliary requests must reach HTTP/1 upstream while the model connection is occupied")
				}
			}
			releaseAuxiliary.Do(func() { close(auxiliaryRelease) })
			for range 2 {
				require.NoError(t, <-results)
			}
			auxiliaryEntry, err := upstream.getClientEntry("", 71, 1, auxiliaryProfile, false, false)
			require.NoError(t, err)
			require.NotSame(t, modelEntry, auxiliaryEntry)
			require.Zero(t, auxiliaryEntry.client.Transport.(*http.Transport).MaxConnsPerHost)
			require.Equal(t, int64(1), atomic.LoadInt64(&modelEntry.inFlight), "auxiliary completion must not release the model's connection accounting")
			unchangedModel, err := upstream.getClientEntry("", 71, 1, modelProfile, false, false)
			require.NoError(t, err)
			require.Same(t, modelEntry, unchangedModel, "auxiliary requests must not rebuild the model pool")
			unchangedAuxiliary, err := upstream.getClientEntry("", 71, 19, auxiliaryProfile, false, false)
			require.NoError(t, err)
			require.Same(t, auxiliaryEntry, unchangedAuxiliary, "model concurrency changes must not rebuild the auxiliary pool")
		})
	}
}

func TestCodexAuxiliaryTransportPreservesOpenAIPolicyAndTLSIsolation(t *testing.T) {
	for _, proxyURL := range []string{"", "http://proxy.invalid:8080", "https://proxy.invalid:8443", "socks5://proxy.invalid:1080"} {
		t.Run(proxyURL, func(t *testing.T) {
			upstream := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{
				ConnectionPoolIsolation:     config.ConnectionPoolIsolationAccountProxy,
				OpenAIHTTP2:                 config.GatewayOpenAIHTTP2Config{Enabled: true},
				OpenAIResponseHeaderTimeout: 42,
				MaxConnsPerHost:             1,
			}}).(*httpUpstreamService)
			for _, fingerprint := range []*tlsfingerprint.Profile{nil, {Name: "test"}} {
				get := func(profile service.HTTPUpstreamProfile, concurrency int) (*upstreamClientEntry, error) {
					if fingerprint != nil {
						return upstream.getClientEntryWithTLS(proxyURL, 71, concurrency, fingerprint, profile, false, false)
					}
					return upstream.getClientEntry(proxyURL, 71, concurrency, profile, false, false)
				}
				model, err := get(service.HTTPUpstreamProfileOpenAI, 1)
				require.NoError(t, err)
				auxiliary, err := get(service.HTTPUpstreamProfileCodexAuxiliary, 1)
				require.NoError(t, err)
				require.NotSame(t, model, auxiliary)
				modelTransport, auxiliaryTransport := model.client.Transport.(*http.Transport), auxiliary.client.Transport.(*http.Transport)
				require.Equal(t, 1, modelTransport.MaxConnsPerHost)
				require.Zero(t, auxiliaryTransport.MaxConnsPerHost)
				require.Equal(t, modelTransport.ForceAttemptHTTP2, auxiliaryTransport.ForceAttemptHTTP2)
				require.Equal(t, modelTransport.ResponseHeaderTimeout, auxiliaryTransport.ResponseHeaderTimeout)
				require.Equal(t, 42*time.Second, auxiliaryTransport.ResponseHeaderTimeout)
				require.Equal(t, model.proxyKey, auxiliary.proxyKey)
				modelAgain, err := get(service.HTTPUpstreamProfileOpenAI, 1)
				require.NoError(t, err)
				require.Same(t, model, modelAgain)
				auxiliaryAgain, err := get(service.HTTPUpstreamProfileCodexAuxiliary, 0)
				require.NoError(t, err)
				require.Same(t, auxiliary, auxiliaryAgain)
			}
		})
	}
}
