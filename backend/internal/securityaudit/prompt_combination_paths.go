package securityaudit

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Offsets refer to the immutable normalized input. A removed path is a hard
// matching boundary, not whitespace: even administrator regexes using (?s).*
// cannot join text on opposite sides. HTTP(S) hostnames are isolated spans.
// Regex ^/$ anchors therefore refer to each retained region independently.
type combinationTextSpan struct {
	start, end int
}

// Dense short paths can leave millions of one-byte whitespace spans. Store
// skip/keep lengths as uvarints (usually two bytes per span), not two machine
// words. Fixed blocks avoid repeatedly copying a growing request-sized slice.
// Blocks belong only to this prepared request and are never pooled.
const combinationSpanBlockBytes = 4080

type combinationSpanBlock struct {
	data [combinationSpanBlockBytes]byte
	used int
	next *combinationSpanBlock
}

type combinationTextView struct {
	inline      [64]byte
	inlineUsed  int
	head, tail  *combinationSpanBlock
	previousEnd int
	spanCount   int
}

func (view *combinationTextView) appendSpan(start, end int) {
	if start >= end {
		return
	}
	view.writeUnsigned(uint64(start - view.previousEnd))
	view.writeUnsigned(uint64(end - start))
	view.previousEnd = end
	view.spanCount++
}

func (view *combinationTextView) writeUnsigned(value uint64) {
	for {
		encoded := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			encoded |= 0x80
		}
		if view.inlineUsed < len(view.inline) {
			view.inline[view.inlineUsed] = encoded
			view.inlineUsed++
		} else {
			if view.tail == nil || view.tail.used == len(view.tail.data) {
				block := new(combinationSpanBlock)
				if view.tail == nil {
					view.head = block
				} else {
					view.tail.next = block
				}
				view.tail = block
			}
			view.tail.data[view.tail.used] = encoded
			view.tail.used++
		}
		if value == 0 {
			return
		}
	}
}

type combinationSpanIterator struct {
	inline      []byte
	block       *combinationSpanBlock
	position    int
	previousEnd int
}

func (view *combinationTextView) iterator() combinationSpanIterator {
	return combinationSpanIterator{inline: view.inline[:view.inlineUsed], block: view.head}
}

func (iterator *combinationSpanIterator) next() (combinationTextSpan, bool) {
	skip, ok := iterator.readUnsigned()
	if !ok {
		return combinationTextSpan{}, false
	}
	keep, ok := iterator.readUnsigned()
	if !ok {
		return combinationTextSpan{}, false
	}
	start := iterator.previousEnd + int(skip)
	iterator.previousEnd = start + int(keep)
	return combinationTextSpan{start: start, end: iterator.previousEnd}, true
}

func (iterator *combinationSpanIterator) readUnsigned() (uint64, bool) {
	var value uint64
	for shift := uint(0); ; shift += 7 {
		if iterator.inline != nil && iterator.position == len(iterator.inline) {
			iterator.inline = nil
			iterator.position = 0
		}
		var encoded byte
		if iterator.inline != nil {
			encoded = iterator.inline[iterator.position]
		} else {
			if iterator.block != nil && iterator.position == iterator.block.used {
				iterator.block = iterator.block.next
				iterator.position = 0
			}
			if iterator.block == nil {
				return 0, false
			}
			encoded = iterator.block.data[iterator.position]
		}
		iterator.position++
		value |= uint64(encoded&0x7f) << shift
		if encoded&0x80 == 0 {
			return value, true
		}
	}
}

var combinationGovernmentHost = regexp.MustCompile(`(?i)^(?:(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*gov\.cn|\.gov\.cn)\.?$`)

// nil means no filtering was necessary; a non-nil empty view means the entire
// input was excluded. Only offsets are retained, never a rewritten text copy.
func combinationTextSpans(text string) *combinationTextView {
	if !strings.ContainsAny(text, `./\:`) {
		return nil
	}
	filter := combinationPathFilter{text: text}
	filter.scan(0, len(text))
	if filter.spans != nil {
		filter.spans.appendSpan(filter.cursor, len(text))
	}
	return filter.spans
}

type combinationPathFilter struct {
	text   string
	cursor int
	spans  *combinationTextView
}

func (filter *combinationPathFilter) exclude(start, end int, host *combinationTextSpan) {
	if filter.spans == nil {
		filter.spans = new(combinationTextView)
	}
	filter.spans.appendSpan(filter.cursor, start)
	if host != nil {
		filter.spans.appendSpan(host.start, host.end)
	}
	filter.cursor = end
}

func (filter *combinationPathFilter) scan(start, end int) {
	for position := start; position < end; {
		r, size := utf8.DecodeRuneInString(filter.text[position:end])
		if combinationPathDelimiter(r) {
			position += size
			continue
		}
		tokenStart := position
		if r == '"' || r == '\'' || r == '`' {
			// An unmatched quote is a delimiter, never a reason to discard all
			// following text. Quoted tokens may contain spaces, but not newlines.
			inner := filter.text[position+1 : end]
			close := strings.IndexByte(inner, byte(r))
			if close < 0 || strings.IndexAny(inner[:close], "\r\n") >= 0 {
				position++
				continue
			}
			position += close + 2
			filter.token(tokenStart, position, true)
			continue
		}
		for position < end {
			r, size = utf8.DecodeRuneInString(filter.text[position:end])
			if combinationPathDelimiter(r) || r == '"' || r == '\'' || r == '`' {
				break
			}
			position += size
		}
		filter.token(tokenStart, position, false)
	}
}

func combinationPathDelimiter(r rune) bool {
	if r < utf8.RuneSelf {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v'
	}
	return unicode.IsSpace(r) || strings.ContainsRune("，。；！？：", r)
}

func (filter *combinationPathFilter) token(start, end int, quoted bool) {
	token := filter.text[start:end]
	value := strings.Trim(token, "\"'`()[]{}<>，。；！？,;!?")
	value = strings.TrimRight(value, ".")
	if value == "" {
		return
	}
	if quoted && strings.ContainsAny(value, `/\`) {
		first, more := combinationFirstWord(value)
		if more && !strings.ContainsAny(first, `/\`) && !combinationDrivePrefix(first) {
			// Preserve prose such as "please attack /tmp/report.txt" while
			// still excluding an entire quoted absolute path containing spaces.
			filter.scan(start+1, end-1)
			return
		}
	}
	// Keep IPv6 authority brackets; only peel paired outer wrappers. The
	// generic filename trim below intentionally has broader punctuation rules.
	urlValue := strings.Trim(token, "\"'`(){}<>，。；！？,;!?")
	urlValue = strings.TrimRight(urlValue, "\"'`(){}<>，。；！？,;!?.")
	for len(urlValue) >= 2 && urlValue[0] == '[' && urlValue[len(urlValue)-1] == ']' {
		urlValue = urlValue[1 : len(urlValue)-1]
	}
	urlValue = strings.TrimRight(urlValue, ".")
	if hostStart, hostEnd, ok := combinationHTTPHost(urlValue); ok {
		// Trim only removes edge bytes, so no decoding/rebuilding is required.
		valueStart := start + strings.Index(token, urlValue)
		host := combinationTextSpan{start: valueStart + hostStart, end: valueStart + hostEnd}
		filter.exclude(start, end, &host)
		return
	}
	if strings.ContainsAny(value, `/\`) || combinationDrivePrefix(value) ||
		(combinationFilename(value) && !combinationGovernmentHost.MatchString(value) && !combinationGovernmentDomainProse(value)) {
		filter.exclude(start, end, nil)
	}
}

// Chinese prose commonly touches a hostname without spaces. Preserve the whole
// token only when a complete ASCII DNS run is surrounded by Han text. Never
// truncate an appended suffix, and keep "gov.cn.备份" classified as a filename.
// This exception is reached only after slash and drive paths were ruled out.
func combinationGovernmentDomainProse(value string) bool {
	position := 0
	for position < len(value) {
		r, size := utf8.DecodeRuneInString(value[position:])
		if !unicode.Is(unicode.Han, r) {
			break
		}
		position += size
	}
	hostStart := position
	for position < len(value) {
		ch := value[position]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			break
		}
		position++
	}
	hostEnd := position
	if hostStart == hostEnd || (hostStart == 0 && hostEnd == len(value)) ||
		(hostEnd < len(value) && value[hostEnd-1] == '.') {
		return false
	}
	for position < len(value) {
		r, size := utf8.DecodeRuneInString(value[position:])
		if !unicode.Is(unicode.Han, r) {
			return false
		}
		position += size
	}
	return combinationGovernmentHost.MatchString(value[hostStart:hostEnd])
}

func combinationFirstWord(value string) (string, bool) {
	start := 0
	for index, r := range value {
		if unicode.IsSpace(r) {
			if index == start {
				start += utf8.RuneLen(r)
				continue
			}
			return value[start:index], strings.TrimSpace(value[index:]) != ""
		}
	}
	return value[start:], false
}

func combinationDrivePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))
}

func combinationFilename(value string) bool {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 || dot == len(value)-1 {
		return false
	}
	return strings.IndexFunc(value[dot+1:], unicode.IsSpace) < 0
}

// Returned byte offsets select the original raw hostname, excluding userinfo,
// port, path, query and fragment. Common ASCII authorities avoid net/url's
// per-token allocations; complex forms still use Parse for validation only.
// Percent escapes (including IPv6 zones) are not decoded into new evidence.
func combinationHTTPHost(value string) (int, int, bool) {
	schemeEnd := 0
	if len(value) >= 7 && strings.EqualFold(value[:7], "http://") {
		schemeEnd = 7
	} else if len(value) >= 8 && strings.EqualFold(value[:8], "https://") {
		schemeEnd = 8
	} else {
		return 0, 0, false
	}
	hostStart, hostEnd := schemeEnd, len(value)
	if delimiter := strings.IndexAny(value[schemeEnd:], "/?#"); delimiter >= 0 {
		hostEnd = schemeEnd + delimiter
	}
	if at := strings.LastIndexByte(value[hostStart:hostEnd], '@'); at >= 0 {
		hostStart += at + 1
	}
	if hostStart >= hostEnd {
		return 0, 0, false
	}
	authorityEnd := hostEnd
	if value[hostStart] == '[' {
		close := strings.IndexByte(value[hostStart:hostEnd], ']')
		if close < 0 {
			return 0, 0, false
		}
		hostEnd = hostStart + close
		hostStart++
	} else if colon := strings.LastIndexByte(value[hostStart:hostEnd], ':'); colon >= 0 {
		hostEnd = hostStart + colon
	}
	if hostStart >= hostEnd {
		return 0, 0, false
	}
	if !combinationHTTPURLSuffixValid(value[authorityEnd:]) {
		return 0, 0, false
	}
	if !combinationSimpleHTTPAuthority(value, schemeEnd, authorityEnd, hostStart, hostEnd) {
		// Validate only the retained authority. Parsing a large escaped path or
		// fragment would allocate a decoded copy that is immediately discarded.
		parsed, err := url.Parse(value[:authorityEnd])
		if err != nil || parsed.Hostname() == "" {
			return 0, 0, false
		}
	}
	return hostStart, hostEnd, true
}

func combinationSimpleHTTPAuthority(value string, authorityStart, authorityEnd, hostStart, hostEnd int) bool {
	if hostStart != authorityStart { // userinfo or bracketed IPv6
		return false
	}
	for index := hostStart; index < hostEnd; index++ {
		ch := value[index]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '.' || ch == '_') {
			return false
		}
	}
	if hostEnd < authorityEnd {
		if value[hostEnd] != ':' {
			return false
		}
		for index := hostEnd + 1; index < authorityEnd; index++ {
			if value[index] < '0' || value[index] > '9' {
				return false
			}
		}
	}
	return true
}

// net/url validates percent escapes in Path and Fragment but leaves RawQuery
// undecoded. Raw controls are rejected before the fragment delimiter; Parse's
// separate setFragment path accepts them. Preserve these exact rules without
// materializing the suffix that the matching view will discard.
func combinationHTTPURLSuffixValid(suffix string) bool {
	const (
		pathPart = iota
		queryPart
		fragmentPart
	)
	part := pathPart
	for index := 0; index < len(suffix); index++ {
		ch := suffix[index]
		if part != fragmentPart && (ch < 0x20 || ch == 0x7f) {
			return false
		}
		switch ch {
		case '?':
			if part == pathPart {
				part = queryPart
			}
		case '#':
			part = fragmentPart
		case '%':
			if part == queryPart {
				continue
			}
			if index+2 >= len(suffix) || !combinationHexDigit(suffix[index+1]) || !combinationHexDigit(suffix[index+2]) {
				return false
			}
			index += 2
		}
	}
	return true
}

func combinationHexDigit(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}
