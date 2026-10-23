//go:build unit

package service

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type shadowsocksConfigRepo struct {
	ProxyRepository
	proxy  *Proxy
	writes int
}

func (r *shadowsocksConfigRepo) Create(_ context.Context, p *Proxy) error {
	r.proxy = p
	r.writes++
	return nil
}

func (r *shadowsocksConfigRepo) GetByID(context.Context, int64) (*Proxy, error) {
	return r.proxy, nil
}

func (r *shadowsocksConfigRepo) Update(_ context.Context, p *Proxy) error {
	r.proxy = p
	r.writes++
	return nil
}

func TestAdminShadowsocksConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, method, password string
		invalid                bool
	}{
		{"AEAD", "aes-256-gcm", " secret@:/?# ", false},
		{"missing method", "", "secret", true},
		{"unsupported method", "invalid", "secret", true},
		{"empty password", "aes-256-gcm", "", true},
		{"oversized password", "aes-256-gcm", strings.Repeat("s", 101), true},
		{"oversized UTF8 password", "aes-256-gcm", strings.Repeat("密", 34), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &shadowsocksConfigRepo{}
			svc := &adminServiceImpl{proxyRepo: repo}
			got, err := svc.CreateProxy(context.Background(), &CreateProxyInput{
				Protocol: "ss", Host: "::1", Port: 8388, Username: tc.method, Password: tc.password,
			})
			if tc.invalid {
				require.Error(t, err)
				require.Zero(t, repo.writes)
				if tc.password != "" {
					require.NotContains(t, err.Error(), tc.password)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.password, got.Password)
			parsed, err := url.Parse(got.URL())
			require.NoError(t, err)
			require.Equal(t, "[::1]:8388", parsed.Host)
			password, ok := parsed.User.Password()
			require.True(t, ok)
			require.Equal(t, tc.password, password)
		})
	}
}

func TestAdminShadowsocksPartialUpdateValidatesFinalCredentials(t *testing.T) {
	password, method, empty := " updated secret ", "chacha20-ietf-poly1305", ""
	repo := &shadowsocksConfigRepo{proxy: &Proxy{Protocol: "ss", Username: "aes-256-gcm", Password: "original"}}
	svc := &adminServiceImpl{proxyRepo: repo}
	got, err := svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{Password: &password})
	require.NoError(t, err)
	require.Equal(t, password, got.Password)
	require.Equal(t, "aes-256-gcm", got.Username)
	got, err = svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{Username: &method})
	require.NoError(t, err)
	require.Equal(t, password, got.Password)
	before := *repo.proxy
	_, err = svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{Password: &empty})
	require.Error(t, err)
	require.Equal(t, before, *repo.proxy)
	require.Equal(t, 2, repo.writes)
	got, err = svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{Protocol: "http", Username: &empty, Password: &password})
	require.NoError(t, err)
	require.Equal(t, "updated secret", got.Password)
	_, err = svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{Protocol: "ss"})
	require.Error(t, err)
	require.Equal(t, "http", repo.proxy.Protocol)
}

func TestShadowsocksSecondaryServiceValidation(t *testing.T) {
	repo := &shadowsocksConfigRepo{}
	svc := NewProxyService(repo)
	_, err := svc.Create(context.Background(), CreateProxyRequest{Protocol: "ss", Username: "aes-256-gcm"})
	require.Error(t, err)
	require.Zero(t, repo.writes)
	_, err = svc.Create(context.Background(), CreateProxyRequest{Protocol: "ss", Username: "aes-256-gcm", Password: "secret"})
	require.NoError(t, err)
	empty := ""
	_, err = svc.Update(context.Background(), 1, UpdateProxyRequest{Username: &empty})
	require.Error(t, err)
	require.Equal(t, "aes-256-gcm", repo.proxy.Username)
	require.Equal(t, 1, repo.writes)
	for _, protocol := range []string{"http", "https", "socks5", "socks5h"} {
		require.NoError(t, ValidateProxyCredentials(protocol, "", ""))
	}
}
