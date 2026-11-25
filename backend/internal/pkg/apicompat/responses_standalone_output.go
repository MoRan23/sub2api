package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Named outputs without a call ID are host-supplied context, not tool replies.
// Keep explicit empty or malformed call IDs on the ordinary tool-result path.
func responsesStandaloneOutputLabel(kind, name string, callID, namespace, output json.RawMessage) string {
	callID = bytes.TrimSpace(callID)
	namespace = bytes.TrimSpace(namespace)
	output = bytes.TrimSpace(output)
	if kind != "function_call_output" || strings.TrimSpace(name) == "" ||
		(len(callID) != 0 && !bytes.Equal(callID, []byte("null"))) ||
		(len(namespace) != 0 && namespace[0] != '"' && !bytes.Equal(namespace, []byte("null"))) ||
		len(output) == 0 || (output[0] != '"' && output[0] != '[') {
		return ""
	}
	provenance, _ := json.Marshal(struct {
		Namespace json.RawMessage `json:"namespace,omitempty"`
		Name      string          `json:"name"`
	}{Namespace: namespace, Name: name})
	return "[Context from " + string(provenance) + "]\n"
}

func rejectResponsesFileImage(imageURL, fileID string) error {
	if strings.TrimSpace(imageURL) == "" && fileID != "" {
		return fmt.Errorf("Responses bridge does not support input_image file_id without image_url; provide an inline image or use a Responses-native upstream")
	}
	return nil
}

// Called only after successful conversion; media occupies a block but has no
// source text path. The provenance label is likewise not caller-supplied text.
func responsesStandaloneOutputSources(raw json.RawMessage, text, prefix string) []string {
	sources := []string{""}
	if len(raw) == 0 {
		if text != "" {
			sources = append(sources, prefix)
		}
		return sources
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return sources
	}
	for i, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text", "":
			if part.Text != "" {
				sources = append(sources, fmt.Sprintf("%s.%d.text", prefix, i))
			}
		default:
			sources = append(sources, "")
		}
	}
	return sources
}
