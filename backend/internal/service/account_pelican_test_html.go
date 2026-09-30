package service

import (
	"strings"

	"golang.org/x/net/html"
)

// ExtractPelicanHTML extracts one explicit complete document without executing
// or judging it. Tokenization ignores HTML inside scripts/comments. Markdown
// fences and surrounding prose are not included in the returned source.
func ExtractPelicanHTML(text string) (string, error) {
	if len(text) > CandyTestMaxResponseBytes {
		return "", candyTestError("response_too_large")
	}
	z := html.NewTokenizer(strings.NewReader(text))
	position, start, doctype, doctypeEnd := 0, -1, -1, -1
	body, bodyClosed := false, false
	result := ""
	for {
		kind := z.Next()
		from := position
		position += len(z.Raw())
		if kind == html.ErrorToken {
			break
		}
		if kind == html.DoctypeToken && start < 0 {
			doctype, doctypeEnd = from, position
			continue
		}
		if kind != html.StartTagToken && kind != html.EndTagToken {
			continue
		}
		name, _ := z.TagName()
		switch string(name) {
		case "html":
			if kind == html.StartTagToken {
				if start >= 0 {
					return "", candyTestError("missing_html")
				}
				start, body, bodyClosed = from, false, false
				if doctype >= 0 && strings.TrimSpace(text[doctypeEnd:from]) == "" {
					start = doctype
				}
			} else if start >= 0 {
				if !body || !bodyClosed {
					return "", candyTestError("missing_html")
				}
				document := strings.TrimSpace(text[start:position])
				if result != "" && result != document {
					return "", candyTestError("ambiguous_html")
				}
				result = document
				start, doctype = -1, -1
			}
		case "body":
			if start >= 0 {
				if kind == html.StartTagToken {
					body = true
				} else {
					bodyClosed = body
				}
			}
		}
	}
	if result == "" || start >= 0 {
		return "", candyTestError("missing_html")
	}
	return result, nil
}
