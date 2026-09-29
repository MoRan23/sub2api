package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	candyGradeMissing   = "missing_answer"
	candyGradeAmbiguous = "ambiguous_answer"
	candyGradeInvalid   = "invalid_answer_format"
)

var candyAnswerKeys = [4]string{"q1_fixed", "q2_adaptive", "q3_fixed", "q3_adaptive"}

// CandyAnswerGradeError describes why a completed answer cannot be graded.
// The caller marks it abnormal while retaining the reason, without inventing
// numeric answers or claiming that a particular mathematical count was wrong.
type CandyAnswerGradeError struct {
	Code     string
	Question string
}

func (e *CandyAnswerGradeError) Error() string {
	return fmt.Sprintf("candy answer %s (%s)", e.Code, e.Question)
}

// ExpectedCandyAnswerCounts returns a copy of the grading key. Array order is
// question 1 fixed, question 2 adaptive, question 3 fixed, question 3 adaptive.
func ExpectedCandyAnswerCounts() [4]int { return [4]int{32, 29, 40, 38} }

var (
	candyQuestionChinese = regexp.MustCompile(`^(?:第)?([123一二三])(?:问|题)(.*)$`)
	candyQuestionPrefix  = regexp.MustCompile(`^(?:question|问题|题目|题号|q)([123一二三])(?:问|题)?(.*)$`)
	candyQuestionNumber  = regexp.MustCompile(`^([123一二三])(?:[.、:()\-]|$)(.*)$`)
	candyRestrictedQ2    = regexp.MustCompile(`先.*(?:圆|r).*(?:再|后).*(?:非圆|n)`)
	candyNumericCell     = regexp.MustCompile(`^(?:最少|至少|共|总共|总计|合计|答案为|答案是)?([+-]?[0-9]+|[零〇一二两兩三四五六七八九十百千万萬壹贰貳叁參肆伍陆陸柒捌玖拾佰仟]+)(?:颗糖果|颗糖|颗|粒|枚|个)?$`)
	candyPairedSplit     = regexp.MustCompile(`(?:<br\s*/?>|[;；,，/\n])`)
)

// GradeCandyAnswer parses only labelled table answer cells; it never looks for
// the expected numbers in free text, strategies, proofs, or reasoning. Transport
// completion, truncation, and timeout checks belong to the caller.
func GradeCandyAnswer(text string) (counts [4]int, err error) {
	var seen [4]bool
	questionColumn, countColumn := -1, -1
	put := func(index, count int) error {
		if seen[index] && counts[index] != count {
			return &CandyAnswerGradeError{Code: candyGradeAmbiguous, Question: candyAnswerKeys[index]}
		}
		seen[index], counts[index] = true, count
		return nil
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(candyFullWidth(rawLine))
		// Quoted examples are not the model's answer. Fences themselves may wrap
		// the requested table, so permit the table inside a Markdown fence.
		if strings.HasPrefix(line, ">") {
			continue
		}
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			continue
		}
		cells := candyTableCells(line)
		if len(cells) < 2 {
			questionColumn, countColumn = -1, -1
			continue
		}
		if q, n, ok := candyTableHeader(cells); ok {
			questionColumn, countColumn = q, n
			continue
		}
		if candyTableSeparator(cells) || questionColumn < 0 || countColumn < 0 {
			continue
		}
		if questionColumn >= len(cells) || countColumn >= len(cells) {
			continue
		}
		question, modifier := candyQuestion(cells[questionColumn])
		if question == 0 {
			continue
		}
		// The puzzle explicitly discusses a restricted two-phase Q2 strategy.
		// An extra row assessing that strategy is not the requested optimum.
		if question == 2 && candyIsRestrictedQ2(modifier) {
			continue
		}
		fixed, adaptive := candyAnswerModes(modifier)
		value := cells[countColumn]
		if question == 3 && ((!fixed && !adaptive) || (fixed && adaptive)) {
			pair, ok := candyParsePair(value)
			if !ok {
				return counts, &CandyAnswerGradeError{Code: candyGradeAmbiguous, Question: "q3"}
			}
			for i, count := range pair {
				if err := put(i+2, count); err != nil {
					return counts, err
				}
			}
			continue
		}
		if (question == 1 && adaptive) || (question == 2 && fixed) {
			return counts, &CandyAnswerGradeError{Code: candyGradeAmbiguous, Question: fmt.Sprintf("q%d", question)}
		}
		index := question - 1
		if question == 3 && adaptive {
			index = 3
		}
		count, ok := candyParseCount(value)
		if !ok {
			return counts, &CandyAnswerGradeError{Code: candyGradeInvalid, Question: candyAnswerKeys[index]}
		}
		if err := put(index, count); err != nil {
			return counts, err
		}
	}
	for index, found := range seen {
		if !found {
			return counts, &CandyAnswerGradeError{Code: candyGradeMissing, Question: candyAnswerKeys[index]}
		}
	}
	return counts, nil
}

func candyFullWidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '\uff01' && r <= '\uff5e' {
			return r - 0xfee0
		}
		if r == '\u3000' {
			return ' '
		}
		return r
	}, s)
}

func candyCompact(s string) string {
	s = strings.ToLower(candyFullWidth(s))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("*_`$", r) {
			return -1
		}
		return r
	}, s)
}

func candyTableCells(line string) []string {
	if !strings.Contains(line, "|") {
		if strings.Contains(line, "\t") {
			return strings.Split(line, "\t")
		}
		return nil
	}
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cell strings.Builder
	escaped := false
	for _, r := range line {
		if escaped {
			if r != '|' {
				_, _ = cell.WriteRune('\\')
			}
			_, _ = cell.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			_, _ = cell.WriteRune(r)
		}
	}
	if escaped {
		_, _ = cell.WriteRune('\\')
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

func candyTableHeader(cells []string) (questionColumn, countColumn int, ok bool) {
	questionColumn, countColumn = -1, -1
	for index, cell := range cells {
		name := candyCompact(cell)
		switch name {
		case "问题", "题目", "题号", "问法", "question":
			if questionColumn >= 0 {
				return -1, -1, false
			}
			questionColumn = index
		case "最少数量", "最少颗数", "最少总数", "最少总颗数", "最少摸取数", "最少摸取颗数", "最少需要颗数", "数量", "颗数", "答案", "答案(颗)", "最少数量(颗)", "最少颗数(颗)", "minimum", "minimumcount", "count":
			if countColumn >= 0 {
				return -1, -1, false
			}
			countColumn = index
		}
	}
	return questionColumn, countColumn, questionColumn >= 0 && countColumn >= 0
}

func candyTableSeparator(cells []string) bool {
	for _, cell := range cells {
		cell = strings.Trim(candyCompact(cell), ":")
		if cell == "" || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func candyQuestion(label string) (int, string) {
	label = candyCompact(label)
	for _, pattern := range []*regexp.Regexp{candyQuestionChinese, candyQuestionPrefix, candyQuestionNumber} {
		if match := pattern.FindStringSubmatch(label); match != nil {
			for index, symbols := range []string{"1一", "2二", "3三"} {
				if strings.Contains(symbols, match[1]) {
					return index + 1, match[2]
				}
			}
		}
	}
	return 0, ""
}

func candyIsRestrictedQ2(modifier string) bool {
	// Explicitly describing the unrestricted optimum must not be mistaken for
	// the extra restricted-strategy comparison row.
	for _, marker := range []string{"不受限制", "不受限", "非受限", "无限制", "不限制", "非两阶段", "不是两阶段"} {
		modifier = strings.ReplaceAll(modifier, marker, "")
	}
	for _, marker := range []string{"受限", "限制", "单向", "单次切换", "两阶段", "先r后n", "先圆后非圆", "先圆感后非圆感"} {
		if strings.Contains(modifier, marker) {
			return true
		}
	}
	return candyRestrictedQ2.MatchString(modifier)
}

func candyAnswerModes(s string) (fixed, adaptive bool) {
	s = candyCompact(s)
	for _, marker := range []string{"非自适应", "不自适应", "non-adaptive", "nonadaptive"} {
		if strings.Contains(s, marker) {
			fixed = true
			s = strings.ReplaceAll(s, marker, "")
		}
	}
	for _, marker := range []string{"固定", "预先", "事先", "第1问", "第一问", "fixed"} {
		fixed = fixed || strings.Contains(s, marker)
	}
	for _, marker := range []string{"自适应", "逐颗", "逐步", "动态", "第2问", "第二问", "adaptive"} {
		adaptive = adaptive || strings.Contains(s, marker)
	}
	return fixed, adaptive
}

func candyParseCount(cell string) (int, bool) {
	match := candyNumericCell.FindStringSubmatch(candyCompact(cell))
	if match == nil {
		return 0, false
	}
	if value, err := strconv.ParseInt(match[1], 10, 32); err == nil {
		return int(value), true
	}
	return candyChineseNumber(match[1])
}

func candyChineseNumber(s string) (int, bool) {
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '壹': 1, '二': 2, '两': 2, '兩': 2, '贰': 2, '貳': 2, '三': 3, '叁': 3, '參': 3, '四': 4, '肆': 4, '五': 5, '伍': 5, '六': 6, '陆': 6, '陸': 6, '七': 7, '柒': 7, '八': 8, '捌': 8, '九': 9, '玖': 9}
	units := map[rune]int{'十': 10, '拾': 10, '百': 100, '佰': 100, '千': 1000, '仟': 1000, '万': 10000, '萬': 10000}
	// Digit-only Chinese notation (三二 / 三〇) is also unambiguous.
	if !strings.ContainsAny(s, "十拾百佰千仟万萬") {
		value := 0
		for _, r := range s {
			digit, ok := digits[r]
			if !ok || value > 214748363 {
				return 0, false
			}
			value = value*10 + digit
		}
		return value, s != ""
	}
	// Candy counts are small. Accept canonical units through thousands rather
	// than guessing values for malformed sequences such as 三十二十九.
	value, pending, previousUnit := 0, -1, 10000
	zeroAfterUnit := false
	for _, r := range s {
		if digit, ok := digits[r]; ok {
			if pending >= 0 && pending != 0 {
				return 0, false
			}
			pending = digit
			if digit == 0 && value > 0 {
				zeroAfterUnit = true
			}
			continue
		}
		unit, ok := units[r]
		if !ok || unit >= previousUnit || (pending < 0 && (unit != 10 || value != 0)) || pending == 0 {
			return 0, false
		}
		if pending < 0 {
			pending = 1
		}
		value += pending * unit
		pending, previousUnit = -1, unit
		zeroAfterUnit = false
	}
	if pending >= 0 {
		// Colloquial 二百三 can mean 203 or 230. Do not guess; explicit
		// 二百零三 / 二百三十 are both unambiguous.
		if pending > 0 && previousUnit > 10 && !zeroAfterUnit {
			return 0, false
		}
		value += pending
	}
	return value, value > 0
}

func candyParsePair(cell string) (counts [2]int, ok bool) {
	parts := candyPairedSplit.Split(strings.ToLower(candyFullWidth(cell)), -1)
	if len(parts) != 2 {
		return counts, false
	}
	var seen [2]bool
	for _, part := range parts {
		part = candyCompact(part)
		fixed, adaptive := candyAnswerModes(part)
		if fixed == adaptive {
			return counts, false
		}
		index := 0
		if adaptive {
			index = 1
		}
		// Remove only recognized mode labels and punctuation. Any remaining
		// explanation or multiple quantities is rejected by the scalar parser.
		for _, label := range []string{"非自适应", "不自适应", "non-adaptive", "nonadaptive", "自适应取法", "固定取法", "自适应", "固定", "第1问规则", "第2问规则", "第一问规则", "第二问规则", "第1问", "第2问", "第一问", "第二问", "adaptive", "fixed"} {
			part = strings.ReplaceAll(part, label, "")
		}
		part = strings.Map(func(r rune) rune {
			if strings.ContainsRune(":()[]", r) {
				return -1
			}
			return r
		}, part)
		count, valid := candyParseCount(part)
		if !valid || seen[index] {
			return counts, false
		}
		seen[index], counts[index] = true, count
	}
	return counts, seen[0] && seen[1]
}
