package service

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/gin-gonic/gin"
)

// Business gateway tests stand in for authenticated handlers. Keep the API key
// stable across requests in one test and isolated from other test lineages.
func setOpenAIDownstreamIdentityTestAPIKey(t testing.TB, c *gin.Context) {
	t.Helper()
	if c == nil {
		t.Fatal("downstream identity test requires a request context")
	}
	if current, exists := c.Get("api_key"); exists {
		if key, ok := current.(*APIKey); ok && key.ID > 0 {
			return
		}
	}
	digest := sha256.Sum256([]byte("openai-downstream-test:" + t.Name()))
	id := int64(binary.BigEndian.Uint64(digest[:8]) & ((1 << 63) - 1))
	if id == 0 {
		id = 1
	}
	c.Set("api_key", &APIKey{ID: id})
}
