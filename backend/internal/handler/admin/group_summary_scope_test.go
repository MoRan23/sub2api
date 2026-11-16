package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseGroupSummaryIDs(t *testing.T) {
	for _, tc := range []struct {
		query           string
		ids             []int64
		scoped, invalid bool
	}{
		{"", nil, false, false}, {"?group_ids=", []int64{}, true, false},
		{"?group_ids=9,2,9", []int64{2, 9}, true, false},
		{"?group_ids=1,0", nil, true, true}, {"?group_ids=all", nil, true, true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/summary"+tc.query, nil)
			ids, scoped, err := parseGroupSummaryIDs(c)
			require.Equal(t, tc.scoped, scoped)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.ids, ids)
			}
		})
	}
}
