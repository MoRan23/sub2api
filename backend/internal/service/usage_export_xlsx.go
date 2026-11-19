package service

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const usageExportColumnCount = 37

// The worksheet is written directly into its ZIP entry. Memory is independent
// of the number of rows; inline strings avoid a workbook-sized string table.
type usageXLSX struct {
	archive *zip.Writer
	sheet   *bufio.Writer
	rows    int
	err     error
	closed  bool
}

func newUsageXLSX(output io.Writer, language string) (UsageExportWriter, error) {
	x := &usageXLSX{archive: zip.NewWriter(output)}
	parts := []struct{ name, contents string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Usage" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
	}
	for _, part := range parts {
		entry, err := x.archive.Create(part.name)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(entry, part.contents); err != nil {
			return nil, err
		}
	}
	entry, err := x.archive.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	x.sheet = bufio.NewWriterSize(entry, 64<<10)
	x.write(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetData>`)
	if err = x.WriteRow(usageExportHeaders(language)); err != nil {
		return nil, err
	}
	return x, nil
}

func (x *usageXLSX) write(value string) {
	if x.err == nil {
		_, x.err = x.sheet.WriteString(value)
	}
}

func (x *usageXLSX) WriteRow(values []any) error {
	if x.closed {
		return errors.New("usage workbook is closed")
	}
	if x.err != nil {
		return x.err
	}
	if len(values) != usageExportColumnCount {
		return fmt.Errorf("usage export requires %d columns, got %d", usageExportColumnCount, len(values))
	}
	if x.rows >= 1048576 {
		return errors.New("usage export exceeds Excel row limit")
	}
	x.rows++
	row := strconv.Itoa(x.rows)
	x.write(`<row r="` + row + `">`)
	for i, value := range values {
		if value == nil {
			continue
		}
		column := string(rune('A' + i))
		if i >= 26 {
			column = "A" + string(rune('A'+i-26))
		}
		ref := column + row
		var number string
		switch v := value.(type) {
		case int:
			number = strconv.Itoa(v)
		case int64:
			number = strconv.FormatInt(v, 10)
		case float64:
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				number = strconv.FormatFloat(v, 'g', -1, 64)
			}
		}
		if number != "" {
			x.write(`<c r="` + ref + `"><v>` + number + `</v></c>`)
			continue
		}
		text, ok := value.(string)
		if !ok {
			text = fmt.Sprint(value)
		}
		// All strings, including those beginning with =/+/-/@, are literal cells.
		x.write(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">`)
		x.writeCellText(text)
		x.write(`</t></is></c>`)
	}
	x.write(`</row>`)
	return x.err
}

func (x *usageXLSX) writeCellText(text string) {
	units := 0
	start := 0
	for i, r := range text {
		units++
		if r > 0xffff {
			units++
		}
		if units > 32767 {
			x.err = errors.New("usage export cell exceeds Excel text limit")
			return
		}
		escaped := ""
		switch r {
		case '&':
			escaped = "&amp;"
		case '<':
			escaped = "&lt;"
		case '>':
			escaped = "&gt;"
		case '\r':
			escaped = "_x000D_"
		case '_':
			// Preserve literal SpreadsheetML escape-looking text.
			if i+7 <= len(text) && text[i+1] == 'x' && text[i+6] == '_' {
				if _, err := strconv.ParseUint(text[i+2:i+6], 16, 16); err == nil {
					escaped = "_x005F_"
				}
			}
		default:
			if (r < 0x20 && r != '\n' && r != '\t') || r == 0xfffe || r == 0xffff {
				escaped = fmt.Sprintf("_x%04X_", r)
			}
			if r == utf8.RuneError {
				_, n := utf8.DecodeRuneInString(text[i:])
				if n == 1 {
					escaped = "�"
				}
			}
		}
		if escaped != "" {
			x.write(text[start:i])
			x.write(escaped)
			_, n := utf8.DecodeRuneInString(text[i:])
			start = i + n
		}
	}
	x.write(text[start:])
}

func (x *usageXLSX) Close() error {
	if x.closed {
		return x.err
	}
	x.closed = true
	x.write(`</sheetData><autoFilter ref="A1:AK` + strconv.Itoa(x.rows) + `"/></worksheet>`)
	if x.err == nil {
		x.err = x.sheet.Flush()
	}
	if err := x.archive.Close(); x.err == nil {
		x.err = err
	}
	return x.err
}

func usageExportHeaders(language string) []any {
	var headers []string
	if language == "zh" {
		headers = []string{"时间", "用户", "API 密钥", "账户", "请求", "发往上游", "上游响应", "上游响应模型不一致", "请求推理强度", "推理强度", "分组", "入站端点", "上游端点", "类型", "输入 Token", "输出 Token", "缓存读取 Token", "缓存创建 Token", "输入费用", "输出费用", "缓存读取费用", "缓存创建费用", "倍率", "账号倍率", "原始", "用户扣费", "账号计费", "首 Token", "耗时", "请求ID", "上游ID", "User-Agent", "IP", "请求结果", "Token 费用（用户扣费）", "安全拒绝固定费", "上游客户端请求 ID"}
	} else {
		headers = []string{"Time", "User", "API Key", "Account", "Requested", "Sent upstream", "Upstream response", "Response model mismatch", "Requested reasoning effort", "Reasoning Effort", "Group", "Inbound Endpoint", "Upstream Endpoint", "Type", "Input Tokens", "Output Tokens", "Cache Read Tokens", "Cache Creation Tokens", "Input Cost", "Output Cost", "Cache Read Cost", "Cache Creation Cost", "Rate", "Account rate", "Original", "User billed", "Account billed", "First Token", "Duration", "Request ID", "Upstream ID", "User-Agent", "IP", "Request outcome", "Token cost (user billed)", "Fixed safety rejection fee", "Upstream client request ID"}
	}
	row := make([]any, len(headers))
	for i := range headers {
		row[i] = headers[i]
	}
	return row
}

func usageExportString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func usageExportInt(value *int) any {
	if value == nil {
		return ""
	}
	return *value
}
func usageExportEffort(value string) string {
	value = strings.TrimSpace(value)
	normalized := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
	switch normalized {
	case "none", "minimal":
		return "-"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh", "extrahigh":
		return "XHigh"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	case "":
		return "-"
	default:
		return value
	}
}

// Match the existing browser export's Number.toPrecision(4), including zeros.
func usageExportPrecision(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	if value == 0 {
		return "0.000"
	}
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'e', 3, 64), 64)
	exponent := int(math.Floor(math.Log10(math.Abs(rounded))))
	if exponent >= 4 || exponent < -6 {
		parts := strings.Split(strconv.FormatFloat(value, 'e', 3, 64), "e")
		e, _ := strconv.Atoi(parts[1])
		sign := "+"
		if e < 0 {
			sign = "-"
			e = -e
		}
		return parts[0] + "e" + sign + strconv.Itoa(e)
	}
	return strconv.FormatFloat(value, 'f', 3-exponent, 64)
}

func usageExportRow(log *UsageLog, spec UsageExportSpec) []any {
	user, key, account, group := "", "", "", ""
	if log.User != nil {
		user = log.User.Email
	}
	if log.APIKey != nil {
		key = log.APIKey.Name
	}
	if log.Account != nil {
		account = log.Account.Name
	}
	if log.Group != nil {
		group = log.Group.Name
	}
	model := log.RequestedModel
	if model == "" {
		model = log.Model
	}
	upstream := usageExportString(log.UpstreamModel)
	if upstream == "" {
		upstream = model
	}
	requestedEffort := usageExportString(log.RequestedReasoningEffort)
	if requestedEffort == "" {
		requestedEffort = usageExportString(log.ReasoningEffort)
	}
	actualEffort := usageExportString(log.ReasoningEffort)
	if actualEffort == "" {
		actualEffort = requestedEffort
	}
	mismatch := ""
	if log.UpstreamModelMismatch != nil {
		if *log.UpstreamModelMismatch {
			mismatch = "Yes"
			if spec.Language == "zh" {
				mismatch = "是"
			}
		} else {
			mismatch = "No"
			if spec.Language == "zh" {
				mismatch = "否"
			}
		}
	}
	requestType := "Unknown"
	switch log.EffectiveRequestType() {
	case RequestTypeSync:
		requestType = "Sync"
	case RequestTypeStream:
		requestType = "Stream"
	case RequestTypeWSV2:
		requestType = "WS"
	case RequestTypeCyberBlocked:
		requestType = "Cyber"
	case RequestTypeLive:
		requestType = "Live"
	}
	if spec.Language == "zh" {
		switch requestType {
		case "Unknown":
			requestType = "未知"
		case "Sync":
			requestType = "同步"
		case "Stream":
			requestType = "流式"
		case "Cyber":
			requestType = "安全策略"
		}
	}
	fee := 0.0
	result, tokenCost, fixedFee, clientID := "", "", "", ""
	if log.GrokSafetyRejection != nil {
		if f := log.GrokSafetyRejection.Fee; !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 {
			fee = f
		}
		result = "Safety review rejected (request failed)"
		if spec.Language == "zh" {
			result = "安全审核拒绝（请求失败）"
		}
		tokenCost = strconv.FormatFloat(math.Max(0, log.ActualCost-fee), 'f', 8, 64)
		fixedFee = strconv.FormatFloat(fee, 'f', 8, 64)
		clientID = log.GrokSafetyRejection.UpstreamClientRequestID
	}
	accountBase := log.TotalCost
	if log.AccountStatsCost != nil {
		accountBase = *log.AccountStatsCost
	}
	accountRate := 1.0
	if log.AccountRateMultiplier != nil {
		accountRate = *log.AccountRateMultiplier
	}
	accountCost := (accountBase-fee)*accountRate + fee
	if math.IsNaN(accountCost) || math.IsInf(accountCost, 0) {
		accountCost = 0
	}
	fixed := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	return []any{log.CreatedAt.Format(time.RFC3339Nano), user, key, account, model, upstream, usageExportString(log.UpstreamResponseModel), mismatch, usageExportEffort(requestedEffort), usageExportEffort(actualEffort), group, usageExportString(log.InboundEndpoint), usageExportString(log.UpstreamEndpoint), requestType, log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheCreationTokens, fixed(log.InputCost), fixed(log.OutputCost), fixed(log.CacheReadCost), fixed(log.CacheCreationCost), usageExportPrecision(log.RateMultiplier), usageExportPrecision(accountRate), fixed(log.TotalCost), fixed(log.ActualCost), fixed(accountCost), usageExportInt(log.FirstTokenMs), usageExportInt(log.DurationMs), log.RequestID, usageExportString(log.UpstreamRequestID), usageExportString(log.UserAgent), usageExportString(log.IPAddress), result, tokenCost, fixedFee, clientID}
}
