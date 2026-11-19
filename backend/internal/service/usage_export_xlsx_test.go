package service

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func usageWorkbookSheet(t *testing.T, data []byte) []byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	require.Len(t, archive.File, 5)
	var worksheet []byte
	for _, entry := range archive.File {
		r, err := entry.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		decoder := xml.NewDecoder(bytes.NewReader(body))
		for {
			_, err = decoder.Token()
			if err == io.EOF {
				break
			}
			require.NoError(t, err, entry.Name)
		}
		if entry.Name == "xl/worksheets/sheet1.xml" {
			worksheet = body
		}
	}
	require.NotEmpty(t, worksheet)
	return worksheet
}

func TestUsageExportXLSXStreamsLiteralCells(t *testing.T) {
	for _, language := range []string{"en", "zh"} {
		var output bytes.Buffer
		writer, err := newUsageXLSX(&output, language)
		require.NoError(t, err)
		row := make([]any, usageExportColumnCount)
		row[0] = "=HYPERLINK(\"https://example.invalid\")"
		row[1] = "<&>中文\r\n\x00_x000A_"
		row[2] = int64(123)
		row[3] = 4.5
		row[36] = "last"
		require.NoError(t, writer.WriteRow(row))
		require.NoError(t, writer.Close())
		require.NoError(t, writer.Close())
		sheet := string(usageWorkbookSheet(t, output.Bytes()))
		require.Contains(t, sheet, `<c r="A2" t="inlineStr"><is><t xml:space="preserve">=HYPERLINK(`)
		require.Contains(t, sheet, `&lt;&amp;&gt;中文_x000D_`)
		require.Contains(t, sheet, `_x0000__x005F_x000A_`)
		require.Contains(t, sheet, `<c r="C2"><v>123</v></c>`)
		require.Contains(t, sheet, `<c r="D2"><v>4.5</v></c>`)
		require.Contains(t, sheet, `<c r="AK2" t="inlineStr">`)
		require.Contains(t, sheet, `<autoFilter ref="A1:AK2"/>`)
		require.NotContains(t, sheet, `<f>`)
		require.Error(t, writer.WriteRow(row))
	}
}

type usageExportBrokenWriter struct{}

func (usageExportBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestUsageExportXLSXLimitsAndErrors(t *testing.T) {
	writer, err := newUsageXLSX(io.Discard, "en")
	require.NoError(t, err)
	require.Error(t, writer.WriteRow([]any{"short"}))
	row := make([]any, usageExportColumnCount)
	row[0] = strings.Repeat("😀", 16384)
	require.ErrorContains(t, writer.WriteRow(row), "text limit")
	require.Error(t, writer.Close())
	writer, err = newUsageXLSX(usageExportBrokenWriter{}, "en")
	if err == nil {
		err = writer.Close()
	}
	require.True(t, errors.Is(err, io.ErrClosedPipe), "%v", err)
}

func TestUsageExportRowPreservesColumnsAndBilling(t *testing.T) {
	requested, actual, upstream, responseModel, inbound := "max", "xhigh", "mapped", "actual-model", "/v1/responses"
	mismatch, accountRate, accountStats, firstToken := true, 0.0, 2.0, 1234
	log := &UsageLog{
		CreatedAt: time.Date(2026, 11, 16, 1, 2, 3, 400, time.UTC),
		User:      &User{Email: "user@example.invalid"}, APIKey: &APIKey{Name: "key"}, Account: &Account{Name: "account"}, Group: &Group{Name: "group"},
		Model: "stored", RequestedModel: "public", UpstreamModel: &upstream, UpstreamResponseModel: &responseModel, UpstreamModelMismatch: &mismatch,
		RequestedReasoningEffort: &requested, ReasoningEffort: &actual, InboundEndpoint: &inbound, RequestType: RequestTypeStream,
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreationTokens: 4,
		InputCost: 0.0000012, OutputCost: 0.0000023, CacheReadCost: 0.0000034, CacheCreationCost: 0.0000045,
		TotalCost: 1, ActualCost: 0.7, AccountStatsCost: &accountStats, AccountRateMultiplier: &accountRate, FirstTokenMs: &firstToken,
		GrokSafetyRejection: &GrokSafetyRejection{Fee: 0.2, UpstreamClientRequestID: "up-client"},
	}
	row := usageExportRow(log, UsageExportSpec{Language: "en", Timezone: "Asia/Tokyo"})
	require.Len(t, row, usageExportColumnCount)
	require.Equal(t, "2026-11-16T01:02:03.0000004Z", row[0])
	require.Equal(t, []any{"public", "mapped", "actual-model", "Yes", "Max", "XHigh", "group"}, row[4:11])
	require.Equal(t, []any{1, 2, 3, 4}, row[14:18])
	require.Equal(t, []any{"0.000001", "0.000002", "0.000003", "0.000005"}, row[18:22])
	require.Equal(t, []any{"0.000", "0.000", "1.000000", "0.700000", "0.200000"}, row[22:27])
	require.Equal(t, firstToken, row[27])
	require.Equal(t, "", row[28])
	require.Equal(t, []any{"0.50000000", "0.20000000", "up-client"}, row[34:37])
	log.GrokSafetyRejection = nil
	log.AccountStatsCost = nil
	log.AccountRateMultiplier = nil
	log.RequestedReasoningEffort = nil
	log.ReasoningEffort = nil
	row = usageExportRow(log, UsageExportSpec{Language: "zh"})
	require.Equal(t, "是", row[7])
	require.Equal(t, "-", row[8])
	require.Equal(t, "-", row[9])
	require.Equal(t, "流式", row[13])
	require.Equal(t, "1.000000", row[26])
	require.Equal(t, []any{"", "", "", ""}, row[33:])
}

func TestUsageExportPrecisionMatchesBrowser(t *testing.T) {
	for _, tt := range []struct {
		input float64
		want  string
	}{{0, "0.000"}, {1, "1.000"}, {0.1, "0.1000"}, {0.001, "0.001000"}, {10000, "1.000e+4"}, {1e-7, "1.000e-7"}, {1e-6, "0.000001000"}, {9999.5, "1.000e+4"}, {-2.5, "-2.500"}} {
		require.Equal(t, tt.want, usageExportPrecision(tt.input))
	}
	for value, want := range map[string]string{"none": "-", "minimal": "-", "extra-high": "XHigh", "x_high": "XHigh", " maximum ": "maximum"} {
		require.Equal(t, want, usageExportEffort(value))
	}
}

func BenchmarkUsageExportXLSX(b *testing.B) {
	row := usageExportRow(&UsageLog{Model: "model", RequestID: "request", InputTokens: 1200, OutputTokens: 50, CreatedAt: time.Unix(0, 0)}, UsageExportSpec{Language: "en"})
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		writer, err := newUsageXLSX(io.Discard, "en")
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < 1000; i++ {
			if err = writer.WriteRow(row); err != nil {
				b.Fatal(err)
			}
		}
		if err = writer.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
