package service

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func excelRegressionTool() map[string]any {
	return map[string]any{"type": "function_call", "id": "native", "call_id": "call1", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"echo\",\"arguments\":{\"text\":\"hello\"}}"}`}
}

func excelRegressionCommentary() map[string]any {
	item := openAIExcelMessage("assistant", "I will create the file.")
	item["id"], item["phase"] = "message1", "commentary"
	return item
}

func TestExcelCompletionNeverDropsInvalidToolBesideCommentary(t *testing.T) {
	for _, prefix := range []map[string]any{
		excelRegressionCommentary(),
		{"type": "reasoning", "summary": []any{}},
		openAIExcelMessage("assistant", "A partial answer."),
	} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"create a file","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		bad := map[string]any{"type": "function_call", "name": "PRIVATE_TOOL", "call_id": "bad", "arguments": `{"secret":"PRIVATE_ARGUMENT"}`}
		output := []any{prefix, bad, excelRegressionTool()}
		stream := excelSSE("response.output_item.done", map[string]any{"output_index": 0, "item": prefix}) + excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": output}})
		actual, err := excelWrappedBody(t, state, stream)
		require.Error(t, err)
		require.Contains(t, actual, "invalid_tool_call")
		require.NotContains(t, actual, "response.completed")
		require.NotContains(t, actual, "response.function_call_arguments")
		require.NotContains(t, actual, "PRIVATE_TOOL")
		require.NotContains(t, actual, "PRIVATE_ARGUMENT")
	}
}

func TestExcelCompletionRejectsExplicitCommentaryWithoutAction(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.done"} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"create a file","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		actual, err := excelWrappedBody(t, state, excelSSE(kind, map[string]any{"response": map[string]any{"status": "completed", "output": []any{excelRegressionCommentary()}}}))
		require.Error(t, err)
		require.Contains(t, actual, "commentary_without_action")
		require.NotContains(t, actual, "response.completed")
	}
	// A short genuine answer is still valid; do not guess intent from its text.
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"say hello","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
	_, err := excelWrappedBody(t, state, excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{openAIExcelMessage("assistant", "Hello!")}}}))
	require.NoError(t, err)
}

func TestExcelCompletionEmptyOrNonFinalMessageCannotHideCommentary(t *testing.T) {
	for _, extra := range []map[string]any{
		{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}},
		{"type": "message", "role": "assistant", "phase": "analysis", "content": []any{map[string]any{"type": "output_text", "text": "thinking"}}},
		{"type": "message", "role": "user", "content": []any{map[string]any{"type": "output_text", "text": "not an answer"}}},
	} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		actual, err := excelWrappedBody(t, state, excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{excelRegressionCommentary(), extra}}}))
		require.Error(t, err)
		require.Contains(t, actual, "commentary_without_action")
		require.NotContains(t, actual, "response.completed")
	}
}

func TestExcelCompletionRetainsStreamedToolsAndTheirIndexes(t *testing.T) {
	for _, mode := range []string{"full", "omitted", "null", "empty"} {
		t.Run(mode, func(t *testing.T) {
			_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"create a file","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
			message, tool := excelRegressionCommentary(), excelRegressionTool()
			terminal := map[string]any{"status": "completed", "model": "gpt-6-astra", "usage": map[string]any{"output_tokens": 17}}
			switch mode {
			case "full":
				terminal["output"] = []any{message, tool}
			case "null":
				terminal["output"] = nil
			case "empty":
				terminal["output"] = []any{}
			}
			stream := excelSSE("response.output_item.done", map[string]any{"output_index": 0, "item": message}) +
				excelSSE("response.output_item.done", map[string]any{"output_index": 1, "item": tool}) +
				excelSSE("response.completed", map[string]any{"response": terminal})
			actual, err := excelWrappedBody(t, state, stream)
			require.NoError(t, err)
			require.NotContains(t, actual, "run_officejs")
			toolDone := 0
			for _, line := range strings.Split(actual, "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event map[string]any
				require.NoError(t, openAIExcelJSON([]byte(strings.TrimPrefix(line, "data: ")), &event))
				if event["type"] == "response.output_item.done" && openAIExcelMap(event["item"])["type"] == "function_call" {
					toolDone++
					require.Equal(t, json.Number("1"), event["output_index"])
					require.Equal(t, "call1", openAIExcelMap(event["item"])["call_id"])
				}
				if event["type"] == "response.completed" {
					output, ok := openAIExcelMap(event["response"])["output"].([]any)
					require.True(t, ok)
					require.Len(t, output, 2)
					require.Equal(t, "message1", openAIExcelMap(output[0])["id"])
					require.Equal(t, "call1", openAIExcelMap(output[1])["call_id"])
				}
			}
			require.Equal(t, 1, toolDone)
			require.Contains(t, actual, `"output_tokens":17`)
		})
	}
}

func TestExcelCompletionRejectsOmittedOrConflictingNativeItems(t *testing.T) {
	for _, output := range []any{[]any{}, []any{excelRegressionCommentary()}, "invalid"} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		stream := excelSSE("response.output_item.added", map[string]any{"output_index": 0, "item": excelRegressionTool()}) +
			excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": output}})
		actual, err := excelWrappedBody(t, state, stream)
		require.Error(t, err)
		require.NotContains(t, actual, "response.completed")
	}
}

func TestExcelCompletionRequiresUnambiguousItemIndexes(t *testing.T) {
	for _, index := range []any{nil, "0", -1, 0.5, 4096} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		actual, err := excelWrappedBody(t, state, excelSSE("response.output_item.added", map[string]any{"output_index": index, "item": excelRegressionTool()}))
		require.Error(t, err)
		require.Contains(t, actual, "invalid_output_index")
	}
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
	second := excelRegressionTool()
	second["id"], second["call_id"] = "other", "other"
	stream := excelSSE("response.output_item.added", map[string]any{"output_index": 0, "item": excelRegressionTool()}) +
		excelSSE("response.output_item.added", map[string]any{"output_index": 0, "item": second}) +
		excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{second}}})
	actual, err := excelWrappedBody(t, state, stream)
	require.Error(t, err)
	require.Contains(t, actual, "conflicting_output_items")
	require.NotContains(t, actual, "response.completed")
}

func TestExcelCompletionRequiresToolForObservedArgumentEvents(t *testing.T) {
	for _, output := range [][]any{nil, {excelRegressionCommentary()}, {excelRegressionTool()}} {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
		stream := excelSSE("response.function_call_arguments.done", map[string]any{"output_index": 0, "item_id": "native", "arguments": "PRIVATE_ARGUMENT"}) +
			excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": output}})
		actual, err := excelWrappedBody(t, state, stream)
		if len(output) == 1 && openAIExcelMap(output[0])["type"] == "function_call" {
			require.NoError(t, err)
			require.Contains(t, actual, `"call_id":"call1"`)
		} else {
			require.Error(t, err)
			require.NotContains(t, actual, "response.completed")
		}
		require.NotContains(t, actual, "PRIVATE_ARGUMENT")
	}
}

func TestExcelCompletionStopsReaderWithBufferedTrailingEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
		stream := excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}}) + strings.Repeat("data: [DONE]\n\n", 8)
		_, err := excelWrappedBody(t, state, stream)
		require.NoError(t, err)
		synctest.Wait() // The bubble fails if a producer remains blocked on its channel.
	})
}

func TestExcelCompletionKeepaliveAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, w := io.Pipe()
		defer func() { _ = w.Close() }()
		resp, err := WrapOpenAIExcelResponse(ctx, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: r}, state)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		go func() {
			_, _ = io.WriteString(w, excelSSE("response.created", map[string]any{"response": map[string]any{"id": "resp1", "status": "in_progress", "output": []any{}}}))
		}()
		reader := bufio.NewReader(resp.Body)
		readEvent := func() string {
			var result strings.Builder
			for {
				line, err := reader.ReadString('\n')
				require.NoError(t, err)
				_, _ = result.WriteString(line)
				if line == "\n" {
					return result.String()
				}
			}
		}
		require.Contains(t, readEvent(), "response.created")
		time.Sleep(16 * time.Second) // Virtual time; no real upstream or wall-clock wait.
		progress := readEvent()
		require.Contains(t, progress, "response.in_progress")
		require.Contains(t, progress, `"id":"resp1"`)
		require.NotContains(t, progress, "response.completed")
		cancel()
		_, err = io.ReadAll(reader)
		require.Error(t, err)
		synctest.Wait()
	})
}
