package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImportTopupCardsBoundsInputBeforeService(t *testing.T) {
	handler := &PaymentHandler{}
	for _, body := range []string{
		`{"codes":[]}`,
		`{"codes":["` + strings.Repeat("x", 4<<20) + `"]}`,
		`{"codes":[` + strings.Repeat(`"x",`, 1000) + `"x"]}`,
	} {
		writer := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(writer)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Params = gin.Params{{Key: "id", Value: "1"}}
		handler.ImportTopupCards(ctx)
		require.Equal(t, http.StatusBadRequest, writer.Code)
		require.NotContains(t, writer.Body.String(), strings.Repeat("x", 512))
	}
}
