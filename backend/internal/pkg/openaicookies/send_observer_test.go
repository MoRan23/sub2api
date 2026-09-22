package openaicookies

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerSendObserverSeesFinalBoundaryWithoutEnablingCookies(t *testing.T) {
	for _, mode := range []string{"bundle", "bypass", "websocket"} {
		t.Run(mode, func(t *testing.T) {
			ctx := WithScope(context.Background(), testScope)
			ctx = WithBundle(ctx, testBundle(t, time.Now(), &http.Cookie{Name: "__oailb", Value: "saved", Path: "/"}))
			wantCookie := "__oailb=saved"
			if mode != "bundle" {
				ctx = Bypass(ctx)
				wantCookie = "__oailb=client"
			}
			var observed *http.Request
			calls := 0
			ctx = WithSendObserver(ctx, func(request *http.Request) { observed = request; calls++ })
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
			require.NoError(t, err)
			request.Header.Set("Cookie", "__oailb=client")
			if mode == "websocket" {
				request.Header.Set("Upgrade", "websocket")
			}
			require.Equal(t, mode != "websocket", NeedsTransportObserver(request))
			_, err = NewManager().Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				require.Equal(t, wantCookie, outbound.Header.Get("Cookie"))
				if mode == "websocket" {
					require.Zero(t, calls)
				} else {
					require.Equal(t, 1, calls)
					require.Same(t, outbound, observed)
				}
				return &http.Response{StatusCode: http.StatusNoContent}, nil
			})).RoundTrip(request)
			require.NoError(t, err)
			require.Equal(t, "__oailb=client", request.Header.Get("Cookie"))
		})
	}
}
