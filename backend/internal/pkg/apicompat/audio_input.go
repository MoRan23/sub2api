package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/tidwall/gjson"
	"strings"
)

// responsesAudioToChatPart borrows the encoded payload from the decoded URI.
// Do not decode or transcode audio here: unsupported formats need an explicit
// client error instead of a lossy bridge or an untrusted remote fetch.
func responsesAudioToChatPart(uri, fileID string) (ChatContentPart, error) {
	if fileID != "" {
		return ChatContentPart{}, fmt.Errorf("Chat bridge does not support input_audio file_id; provide an inline WAV or MP3 data URI")
	}
	header, payload, ok := strings.Cut(uri, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return ChatContentPart{}, fmt.Errorf("Chat bridge requires input_audio.audio_url to be an inline Base64 WAV or MP3 data URI; remote audio URLs are unsupported")
	}
	mime := header[len("data:") : len(header)-len(";base64")]
	format := ""
	switch strings.ToLower(mime) {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		format = "wav"
	case "audio/mp3", "audio/mpeg":
		format = "mp3"
	default:
		return ChatContentPart{}, fmt.Errorf("Chat bridge supports only WAV or MP3 input_audio")
	}
	if !validInlineAudioBase64(payload) {
		return ChatContentPart{}, fmt.Errorf("input_audio contains empty or invalid Base64 data")
	}
	return ChatContentPart{Type: "input_audio", InputAudio: &ChatInputAudio{Data: payload, Format: format}}, nil
}

func rejectAnthropicAudio(raw json.RawMessage) error {
	// The common text/image path does not parse or copy the content again.
	if !bytes.Contains(raw, []byte("input_audio")) && !bytes.Contains(raw, []byte(`\u`)) {
		return nil
	}
	if responsesContentHasAudio(gjson.Parse(string(raw))) {
		return fmt.Errorf("Anthropic bridge does not support input_audio; use an audio-capable upstream")
	}
	return nil
}

func responsesContentHasAudio(value gjson.Result) bool {
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool { found = responsesContentHasAudio(item); return !found })
		return found
	}
	if value.IsObject() {
		return value.Get("type").String() == "input_audio" || responsesContentHasAudio(value.Get("content"))
	}
	return false
}

// Validate the Base64 alphabet/shape in place without allocating a decoded
// recording. CR/LF have the same handling as encoding/base64.StdEncoding.
func validInlineAudioBase64(data string) bool {
	count, padding := 0, 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '\r' || c == '\n' {
			continue
		}
		count++
		if c == '=' {
			padding++
			if padding > 2 {
				return false
			}
			continue
		}
		if padding != 0 || !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return count > 0 && count%4 == 0
}
