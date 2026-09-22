package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOpsRouteUsesFrozenPhysicalProxy(t *testing.T) {
	originalID := int64(11)
	account := &Account{ID: 7, ProxyID: &originalID, Proxy: &Proxy{ID: originalID, Name: "account route"}}
	for _, tc := range []struct {
		name          string
		frozenAccount int64
		proxy         int64
		wantID        int64
		wantName      string
	}{
		{"ticket proxy", 7, 23, 23, opsProxyNameUnnamed},
		{"frozen direct", 7, 0, 0, opsProxyNameDirect},
		{"original proxy", 7, 11, 11, "account route"},
		{"different account ignored", 8, 23, 11, "account route"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(openAIOutboundRouteKey, frozenOpenAIOutboundRoute{accountID: tc.frozenAccount, route: OpenAIEgressRoute{ProxyID: tc.proxy}})
			id := opsOpenAIOutboundProxyID(c, account)
			if tc.wantID == 0 {
				require.Nil(t, id)
			} else {
				require.NotNil(t, id)
				require.Equal(t, tc.wantID, *id)
				*id = 99
				require.Equal(t, tc.wantID, *opsOpenAIOutboundProxyID(c, account), "diagnostics must not mutate the frozen route")
			}
			require.Equal(t, tc.wantName, opsOpenAIOutboundProxyName(c, account))
		})
	}
	require.Equal(t, originalID, *account.ProxyID)
}
