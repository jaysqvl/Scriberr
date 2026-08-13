package llm

import "context"

const (
	maxProviderResponseBytes = 16 * 1024 * 1024
	maxProviderStreamLine    = 1024 * 1024
)

// Service is a provider-agnostic LLM interface
type Service interface {
	GetModels(ctx context.Context) ([]string, error)
	ChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, model string, messages []ChatMessage, temperature float64) (<-chan string, <-chan error)
	GetContextWindow(ctx context.Context, model string) (int, error)
}
