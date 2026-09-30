package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPelicanPromptIsExactAndVersioned(t *testing.T) {
	require.Equal(t, "pelican-v1", CandyTestPromptVersion)
	require.Equal(t, "创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画，你不需要进行任何测试", CandyTestPrompt)
	require.Equal(t, "请在最终回复中提供完整、自包含的 HTML 源码。", pelicanTestInstructions)
}

func TestExtractPelicanHTML(t *testing.T) {
	doc := `<!DOCTYPE html><html lang="zh"><head><style>svg { animation: ride 1s infinite }</style></head><body><svg><text>鹈鹕</text></svg><script>const sample = "<html><body>nested</body></html>";</script></body></html>`
	for _, tc := range []struct{ name, text, want, code string }{
		{"direct", doc, doc, ""},
		{"fenced", "Here is the HTML:\n```html\n" + doc + "\n```\nDone.", doc, ""},
		{"uppercase", "<HTML><BODY>animation</BODY></HTML>", "<HTML><BODY>animation</BODY></HTML>", ""},
		{"duplicate", doc + "\n" + doc, doc, ""},
		{"comment", "<!-- <html><body>not a document</body></html> -->" + doc, doc, ""},
		{"multiple", doc + "<html><body>another</body></html>", "", "ambiguous_html"},
		{"svg_only", "<svg></svg>", "", "missing_html"},
		{"empty", "", "", "missing_html"},
		{"refusal", "未确定，无法唯一确定", "", "missing_html"},
		{"truncated", "<html><body><svg></svg>", "", "missing_html"},
		{"missing_body_close", "<html><body></html>", "", "missing_html"},
		{"unfinished_second", doc + "<html><body>", "", "missing_html"},
		{"nested_document", "<html><body><html><body></body></html>", "", "missing_html"},
		{"unclosed_script", "<html><body><script> // </body></html>", "", "missing_html"},
		{"too_large", strings.Repeat("x", CandyTestMaxResponseBytes+1), "", "response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractPelicanHTML(tc.text)
			if tc.code != "" {
				require.EqualError(t, err, tc.code)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, got)
		})
	}
}
