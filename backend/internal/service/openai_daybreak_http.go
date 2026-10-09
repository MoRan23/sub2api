package service

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

// prepareOpenAIDaybreakHTTPRequest is the last-resort HTTP send guard, including
// API-key, Engine and diagnostic routes that do not use the OAuth finalizer.
// It never enables automatic injection and never changes the retry source.
func prepareOpenAIDaybreakHTTPRequest(request *http.Request, account *Account, settings *SettingService) error {
	if request == nil || account == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	freezeOpenAIDaybreakPolicyOnRequest(request, settings)
	if openAIDaybreakPolicyEnabled(request.Context(), settings) {
		return nil
	}
	switch openAIDaybreakDecisionFromRequest(request) {
	case "global_disabled", "global_disabled_stripped":
		return nil
	}
	if request.Body == nil || request.Body == http.NoBody {
		setOpenAIDaybreakDecision(request, "global_disabled")
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil && request.Header.Get("Content-Type") != "" {
		return fmt.Errorf("prepare Daybreak request content type: %w", err)
	}
	body, err := openAIDaybreakHTTPRequestBody(request)
	if err != nil {
		return err
	}
	if mediaType != "" && mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "multipart/form-data" && !openAIDaybreakJSONEndpoint(request) {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil
		}
	}
	var updated []byte
	var changed bool
	if mediaType == "multipart/form-data" {
		updated, changed, err = stripOpenAIMultipartCyber(body, params["boundary"])
	} else if len(bytes.TrimSpace(body)) != 0 {
		updated, changed, err = stripOpenAIRequestCyber(body)
	}
	if err != nil {
		return fmt.Errorf("prepare Daybreak request body: %w", err)
	}
	decision := "global_disabled"
	if changed {
		setOpenAIRequestBodySnapshot(request, updated)
		decision = "global_disabled_stripped"
	}
	setOpenAIDaybreakDecision(request, decision)
	return nil
}

func openAIDaybreakJSONEndpoint(request *http.Request) bool {
	if request.URL == nil {
		return false
	}
	path := strings.TrimRight(request.URL.Path, "/")
	for _, suffix := range []string{"/responses", "/responses/compact", "/responses/input_tokens", "/chat/completions", "/messages", "/messages/count_tokens", "/alpha/search", "/images/generations", "/images/edits", "/embeddings"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func openAIDaybreakHTTPRequestBody(request *http.Request) ([]byte, error) {
	if reader, ok := request.Body.(*openAIRequestBodySnapshotReader); ok {
		return reader.snapshot, nil
	}
	if request.GetBody != nil {
		reader, err := request.GetBody()
		if err != nil {
			return nil, fmt.Errorf("read Daybreak request snapshot: %w", err)
		}
		defer func() { _ = reader.Close() }()
		return io.ReadAll(reader)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read Daybreak request body: %w", err)
	}
	_ = request.Body.Close()
	setOpenAIRequestBodySnapshot(request, body)
	return body, nil
}

// Keep the exact multipart bytes for the overwhelmingly common no-op case.
// When a field changes, retain the original boundary, part order, MIME headers
// and file contents; do not decode transfer encodings or treat files as JSON.
func stripOpenAIMultipartCyber(body []byte, boundary string) ([]byte, bool, error) {
	if boundary == "" {
		return body, false, fmt.Errorf("multipart boundary is missing")
	}
	replacements := map[int][]byte{}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for index := 0; ; index++ {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return body, false, err
		}
		_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			return body, false, err
		}
		_, isFile := disposition["filename"]
		if disposition["name"] == "access_programs" && !isFile {
			raw, err := io.ReadAll(part)
			if err != nil {
				return body, false, err
			}
			updated, changed, err := stripOpenAIAccessProgramsCyber(raw)
			if err != nil {
				return body, false, err
			}
			if changed {
				replacements[index] = updated
			}
		}
		if err := part.Close(); err != nil {
			return body, false, err
		}
	}
	if len(replacements) == 0 {
		return body, false, nil
	}
	var output bytes.Buffer
	output.Grow(len(body))
	writer := multipart.NewWriter(&output)
	if err := writer.SetBoundary(boundary); err != nil {
		return body, false, err
	}
	reader = multipart.NewReader(bytes.NewReader(body), boundary)
	for index := 0; ; index++ {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return body, false, err
		}
		destination, err := writer.CreatePart(part.Header)
		if err != nil {
			return body, false, err
		}
		if replacement, ok := replacements[index]; ok {
			_, err = destination.Write(replacement)
		} else {
			_, err = io.Copy(destination, part)
		}
		if err != nil {
			return body, false, err
		}
		if err := part.Close(); err != nil {
			return body, false, err
		}
	}
	if err := writer.Close(); err != nil {
		return body, false, err
	}
	return output.Bytes(), true, nil
}
