package service

import (
	cryptorand "crypto/rand"
	"math/big"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// These templates are deliberately short and provider-neutral. They are used
// only when an OpenAI test/probe did not receive an explicit prompt.
var openAITextTestTemplates = []string{
	"hi",
	"hello",
	"Please respond with a short acknowledgement.",
	"Reply with one concise sentence.",
	"Give a brief confirmation.",
}

var openAICompactTestTemplates = []string{
	"Respond with OK.",
	"Return a short acknowledgement.",
	"Summarize the current context briefly.",
	"Reply with one concise sentence.",
}

var openAIImageTestTemplates = []string{
	"Generate a cute orange cat astronaut sticker on a clean pastel background.",
	"Generate a small watercolor rocket sticker with a clean white background.",
	"Generate a friendly blue whale sticker with simple pastel shapes.",
	"Generate a cheerful mountain sunrise sticker in a minimal paper-cut style.",
}

func randomOpenAITestTemplate(templates []string) string {
	if len(templates) == 0 {
		return ""
	}

	index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(templates))))
	if err != nil {
		return templates[0]
	}
	return templates[index.Int64()]
}

// selectOpenAITestTemplate is deterministic so unit tests can cover every
// template without relying on probability. Production code uses the random
// selector above.
func selectOpenAITestTemplate(templates []string, index int) string {
	if len(templates) == 0 {
		return ""
	}
	if index < 0 || index >= len(templates) {
		index = 0
	}
	return templates[index]
}

func randomOpenAITextTestPrompt() string {
	return randomOpenAITestTemplate(openAITextTestTemplates)
}

func randomOpenAICompactTestPrompt() string {
	return randomOpenAITestTemplate(openAICompactTestTemplates)
}

func randomOpenAIImageTestPrompt() string {
	return randomOpenAITestTemplate(openAIImageTestTemplates)
}

func openAITestInstructions() string {
	return openai.DefaultInstructions
}
