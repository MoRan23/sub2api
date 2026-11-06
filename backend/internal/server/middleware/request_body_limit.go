package middleware

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/gin-gonic/gin"
)

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小。
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		request, release := runtimeobs.Default.TrackRequestBody(c.Request)
		c.Request = request
		defer release()
		c.Next()
	}
}
