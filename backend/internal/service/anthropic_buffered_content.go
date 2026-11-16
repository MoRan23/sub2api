package service

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// Each block owns its builders; slice growth must never copy a used Builder.
type anthropicBufferedBlock struct {
	text, thinking, signature *strings.Builder
	input                     json.RawMessage
	inputChanged              bool
	inputState                uint8 // 0: whitespace; 1: {; 2: {}; 3: not an empty object
}

type anthropicBufferedContent struct {
	response *apicompat.AnthropicResponse
	blocks   []*anthropicBufferedBlock
}

func (a *anthropicBufferedContent) reset(response *apicompat.AnthropicResponse) {
	a.response = response
	a.blocks = make([]*anthropicBufferedBlock, len(response.Content))
}

func (a *anthropicBufferedContent) appendBlock(block apicompat.AnthropicContentBlock) {
	a.response.Content = append(a.response.Content, block)
	a.blocks = append(a.blocks, nil)
}

func appendAnthropicBufferedString(builder **strings.Builder, initial, fragment string) {
	if *builder == nil {
		*builder = new(strings.Builder)
		(*builder).WriteString(initial)
	}
	(*builder).WriteString(fragment)
}

func (b *anthropicBufferedBlock) scanInput(fragment []byte) {
	for _, c := range fragment {
		if b.inputState == 3 {
			return
		}
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		switch {
		case b.inputState == 0 && c == '{':
			b.inputState = 1
		case b.inputState == 1 && c == '}':
			b.inputState = 2
		default:
			b.inputState = 3
		}
	}
}

func (b *anthropicBufferedBlock) appendInput(initial json.RawMessage, fragment string) {
	if !b.inputChanged {
		b.inputChanged = true
		b.scanInput(initial)
		if len(initial) != 0 && b.inputState != 2 {
			b.input = append(b.input, initial...)
		}
	}
	if len(b.input) == 0 || b.inputState == 2 {
		b.input = b.input[:0]
		b.inputState = 0
	}
	b.input = append(b.input, fragment...)
	b.scanInput([]byte(fragment))
}

func (a *anthropicBufferedContent) applyDelta(index int, delta *apicompat.AnthropicDelta, signature bool) {
	if index < 0 || index >= len(a.blocks) {
		return
	}
	if a.blocks[index] == nil {
		a.blocks[index] = new(anthropicBufferedBlock)
	}
	b := a.blocks[index]
	initial := &a.response.Content[index]
	switch delta.Type {
	case "text_delta":
		appendAnthropicBufferedString(&b.text, initial.Text, delta.Text)
	case "thinking_delta":
		appendAnthropicBufferedString(&b.thinking, initial.Thinking, delta.Thinking)
	case "signature_delta":
		if signature {
			appendAnthropicBufferedString(&b.signature, initial.Signature, delta.Signature)
		}
	case "input_json_delta":
		b.appendInput(initial.Input, delta.PartialJSON)
	}
}

func (a *anthropicBufferedContent) finalize() {
	for i, b := range a.blocks {
		if b == nil {
			continue
		}
		block := &a.response.Content[i]
		if b.text != nil {
			block.Text = b.text.String()
		}
		if b.thinking != nil {
			block.Thinking = b.thinking.String()
		}
		if b.signature != nil {
			block.Signature = b.signature.String()
		}
		if b.inputChanged {
			block.Input = b.input
			if block.Input == nil {
				block.Input = json.RawMessage{}
			}
		}
	}
}
