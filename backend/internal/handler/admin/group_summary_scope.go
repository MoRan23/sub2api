package admin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func parseGroupSummaryIDs(c *gin.Context) ([]int64, bool, error) {
	raw, present := c.GetQuery("group_ids")
	// GetQuery treats an explicitly empty value as absent.
	if !present {
		_, present = c.Request.URL.Query()["group_ids"]
	}
	if !present {
		return nil, false, nil
	}
	if raw == "" {
		return []int64{}, true, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 1000 {
		return nil, true, fmt.Errorf("group_ids supports at most 1000 IDs")
	}
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, true, fmt.Errorf("invalid group_ids")
		}
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
			seen[id] = struct{}{}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, true, nil
}
