package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pinnedModelsRoutesRepository struct {
	service.AccountRepository
	account service.Account
}

func (r *pinnedModelsRoutesRepository) ListByGroup(context.Context, int64) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

func (r *pinnedModelsRoutesRepository) ListSchedulableByGroupID(context.Context, int64) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

func (r *pinnedModelsRoutesRepository) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type pinnedModelsRoutesUpstream struct {
	service.HTTPUpstream
	ordinaryCalls atomic.Int32
	codexCalls    atomic.Int32
	codexBody     string
}

func (u *pinnedModelsRoutesUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body := `{"data":[{"id":"ordinary-upstream-model"}]}`
	if req.URL.Query().Has("client_version") {
		u.codexCalls.Add(1)
		body = `{"models":[{"slug":"gpt-5.5"}]}`
		if u.codexBody != "" {
			body = u.codexBody
		}
	} else {
		u.ordinaryCalls.Add(1)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestGatewayRoutesPinnedModelsDispatchesOrdinaryAndCodexRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []struct{ name, body, modelID string }{
		{"manifest", `{"models":[{"slug":"gpt-5.5"}]}`, "gpt-5.5"},
		{"standard list", `{"data":[{"id":"gpt-5.5","provider_extension":true}]}`, "gpt-5.5"},
		{"empty manifest", `{"models":[]}`, ""},
		{"empty standard list", `{"data":[]}`, ""},
	} {
		t.Run(source.name, func(t *testing.T) {
			repo := &pinnedModelsRoutesRepository{account: service.Account{
				ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true,
				Credentials: map[string]any{"api_key": "test-models-key", "base_url": "https://models.example/v1"},
			}}
			upstream := &pinnedModelsRoutesUpstream{codexBody: source.body}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			s := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg,
				nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &handler.Handlers{
				Gateway:       handler.NewGatewayHandler(nil, s, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil),
				OpenAIGateway: handler.NewOpenAIGatewayHandler(s, nil, nil, nil, nil, nil, nil, nil, cfg),
				AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
			}
			group := &service.Group{ID: 1, Platform: service.PlatformOpenAI,
				CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{7}}}
			router := gin.New()
			RegisterGatewayRoutes(router, h, servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
				c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
				c.Next()
			}), nil, nil, nil, nil, nil, cfg)
			var standardETag, codexETag string
			for _, path := range []string{"/v1/models", "/models", "/v1/models?client_version=", "/models?client_version="} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
				var response struct {
					Object string `json:"object"`
					Data   []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
				require.Equal(t, "list", response.Object)
				if source.modelID == "" {
					require.Empty(t, response.Data, "successful empty fixed sources must not borrow public models")
				} else {
					require.Len(t, response.Data, 1)
					require.Equal(t, source.modelID, response.Data[0].ID)
				}
				require.NotContains(t, w.Body.String(), "ordinary-upstream-model")
				if standardETag == "" {
					standardETag = w.Header().Get("ETag")
				}
				require.Equal(t, standardETag, w.Header().Get("ETag"))
			}
			for _, path := range []string{"/v1/models?client_version=" + service.CodexCanonicalClientVersion(), "/models?client_version=" + service.CodexCanonicalClientVersion(), "/backend-api/codex/models"} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
				var manifest struct {
					Models []struct {
						Slug string `json:"slug"`
					} `json:"models"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &manifest))
				if source.modelID == "" {
					require.Empty(t, manifest.Models, "successful empty fixed sources must not borrow public models")
				} else {
					require.Len(t, manifest.Models, 1)
					require.Equal(t, source.modelID, manifest.Models[0].Slug)
				}
				require.NotContains(t, w.Body.String(), `"data"`)
				if codexETag == "" {
					codexETag = w.Header().Get("ETag")
				}
				require.Equal(t, codexETag, w.Header().Get("ETag"))
			}
			require.NotEmpty(t, standardETag)
			require.NotEmpty(t, codexETag)
			require.NotEqual(t, standardETag, codexETag)
			for _, request := range []struct {
				path, etag string
				status     int
			}{
				{"/v1/models", standardETag, http.StatusNotModified},
				{"/backend-api/codex/models", codexETag, http.StatusNotModified},
				{"/v1/models", codexETag, http.StatusOK},
				{"/backend-api/codex/models", standardETag, http.StatusOK},
			} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, request.path, nil)
				req.Header.Set("If-None-Match", request.etag)
				router.ServeHTTP(w, req)
				require.Equal(t, request.status, w.Code, "%s: %s", request.path, w.Body.String())
				if request.status == http.StatusNotModified {
					require.Empty(t, w.Body.Bytes())
				}
			}
			require.Zero(t, upstream.ordinaryCalls.Load(), "both representations use the same fixed source")
			require.EqualValues(t, 1, upstream.codexCalls.Load())
		})
	}
}

func TestGatewayRoutesRetrievePinnedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &pinnedModelsRoutesRepository{account: service.Account{
		ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "test-models-key", "base_url": "https://models.example/v1"},
	}}
	upstream := &pinnedModelsRoutesUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	s := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &handler.Handlers{
		Gateway:       handler.NewGatewayHandler(nil, s, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil),
		OpenAIGateway: handler.NewOpenAIGatewayHandler(s, nil, nil, nil, nil, nil, nil, nil, cfg),
		AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
	}
	group := &service.Group{ID: 1, Platform: service.PlatformOpenAI,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{7}}}
	router := gin.New()
	RegisterGatewayRoutes(router, h, servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer client-key" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
		c.Next()
	}), nil, nil, nil, nil, nil, cfg)
	request := func(path, key, etag string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", key)
		req.Header.Set("If-None-Match", etag)
		router.ServeHTTP(w, req)
		return w
	}
	list := request("/v1/models", "Bearer client-key", "")
	require.Equal(t, http.StatusOK, list.Code)
	var catalog struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &catalog))
	require.Len(t, catalog.Data, 1)
	for _, base := range []string{"/v1/models/", "/models/"} {
		path := base + "gpt-5.5?client_version=ignored"
		require.Equal(t, http.StatusUnauthorized, request(path, "", "").Code)
		got := request(path, "Bearer client-key", list.Header().Get("ETag"))
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		require.JSONEq(t, string(catalog.Data[0]), got.Body.String())
		require.Empty(t, got.Header().Get("ETag"), "a collection validator cannot validate one model")
		missing := request(base+"unknown-model", "Bearer client-key", "")
		require.Equal(t, http.StatusNotFound, missing.Code)
		require.Contains(t, missing.Body.String(), `"error"`)
	}
	require.Zero(t, upstream.ordinaryCalls.Load(), "retrieve shares the fixed catalog source")
	require.EqualValues(t, 1, upstream.codexCalls.Load(), "retrieve reuses the catalog without forwarding its client_version query")
}
