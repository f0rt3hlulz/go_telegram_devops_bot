package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"go_devops_telegram_bot/internal/questions"
)

const (
	systemPrompt = `You are an experienced DevOps interviewer. Create challenging multiple-choice interview questions suitable for mid to senior engineers.
- Ground every question in real-world tooling or practices (Ansible, Docker, Linux, Kubernetes, GitLab CI, Bash, Python, Nginx, HAProxy, Grafana, Prometheus, ELK, SQL, ClickHouse, and related DevOps topics).
- Focus on depth: configuration gotchas, performance tuning, production incident handling, architecture choices, CI/CD troubleshooting, observability, etc.
- Prefer topics named by the user when provided; otherwise rotate through the domains to keep variety.
- Always produce four answer options with only one correct answer. Make the distractors plausible but clearly wrong for an expert.
- Answers must reveal why they are correct in a concise explanation.
- Explanations must teach: include actionable guidance, common pitfalls, and next steps for deeper mastery.`
	defaultModel               = "gpt-5"
	defaultTemperature         = 1.0
	defaultPromptCostPer1K     = 0.01
	defaultCompletionCostPer1K = 0.03
	requestTimeout             = 90 * time.Second
)

// Result represents the outcome of a generator call, including token usage and estimated cost.
type Result struct {
	Question         questions.Question
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	Language         string
}

// Config contains the OpenAI generator settings.
type Config struct {
	APIKey              string
	BaseURL             string
	Model               string
	Temperature         float32
	PromptCostPer1K     float64
	CompletionCostPer1K float64
}

// OpenAIGenerator fetches questions from OpenAI's chat completion endpoint.
type OpenAIGenerator struct {
	client              *openai.Client
	model               string
	temperature         float32
	promptCostPer1K     float64
	completionCostPer1K float64
}

// NewOpenAIGenerator returns a generator configured with the provided settings.
func NewOpenAIGenerator(cfg Config) (*OpenAIGenerator, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("missing OpenAI API key")
	}
	clientConfig := openai.DefaultConfig(cfg.APIKey)
	if strings.TrimSpace(cfg.BaseURL) != "" {
		clientConfig.BaseURL = strings.TrimSpace(cfg.BaseURL)
	}

	client := openai.NewClientWithConfig(clientConfig)

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultModel
	}
	temp := cfg.Temperature
	if temp <= 0 {
		temp = defaultTemperature
	}

	promptCost := cfg.PromptCostPer1K
	if promptCost <= 0 {
		promptCost = defaultPromptCostPer1K
	}
	completionCost := cfg.CompletionCostPer1K
	if completionCost <= 0 {
		completionCost = defaultCompletionCostPer1K
	}

	return &OpenAIGenerator{
		client:              client,
		model:               model,
		temperature:         temp,
		promptCostPer1K:     promptCost,
		completionCostPer1K: completionCost,
	}, nil
}

// Generate requests a new interview question from OpenAI. The call respects the provided context and times out automatically.
func (g *OpenAIGenerator) Generate(ctx context.Context, topic, language string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	lang := strings.TrimSpace(language)
	if lang == "" {
		lang = "English"
	}
	payload := buildPrompt(topic, lang)

	resp, err := g.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       g.model,
		Temperature: g.temperature,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemPrompt,
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: payload,
			},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return Result{}, err
	}

	if len(resp.Choices) == 0 {
		return Result{}, errors.New("empty completion choices")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		return Result{}, errors.New("empty completion content")
	}

	question, err := parseQuestionJSON(content)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Question: question,
		Model:    g.model,
		Language: lang,
	}

	usage := resp.Usage
	result.PromptTokens = usage.PromptTokens
	result.CompletionTokens = usage.CompletionTokens
	result.TotalTokens = usage.TotalTokens
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		inputCost := (float64(result.PromptTokens) / 1000.0) * g.promptCostPer1K
		outputCost := (float64(result.CompletionTokens) / 1000.0) * g.completionCostPer1K
		result.CostUSD = inputCost + outputCost
	}

	return result, nil
}

func buildPrompt(topic, language string) string {
	topic = strings.TrimSpace(topic)
	lang := strings.TrimSpace(language)
	if lang == "" {
		lang = "English"
	}
	template := `Produce a single new interview question grounded in the listed DevOps domains. The entire output must be written in %s. Structure the explanation as a mini lesson: start with a concise summary, then provide step-by-step reasoning, and finish with bullet-point practical takeaways. Respond strictly as JSON with keys: topic, level, prompt, options (array of 4 strings), answer (must match one option exactly), explanation.`
	if topic != "" {
		template = `Produce a single new interview question focused on %q. The entire output must be written in %s. Structure the explanation as a mini lesson: start with a concise summary, then provide step-by-step reasoning, and finish with bullet-point practical takeaways. Respond strictly as JSON with keys: topic, level, prompt, options (array of 4 strings), answer (must match one option exactly), explanation.`
		return fmt.Sprintf(template, topic, lang)
	}
	return fmt.Sprintf(template, lang)
}

type rawQuestion struct {
	Topic       string   `json:"topic"`
	Level       string   `json:"level"`
	Prompt      string   `json:"prompt"`
	Options     []string `json:"options"`
	Answer      string   `json:"answer"`
	Explanation string   `json:"explanation"`
}

func parseQuestionJSON(payload string) (questions.Question, error) {
	var raw rawQuestion
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return questions.Question{}, fmt.Errorf("decode question JSON: %w", err)
	}

	raw.Topic = strings.TrimSpace(raw.Topic)
	raw.Level = strings.TrimSpace(raw.Level)
	raw.Prompt = strings.TrimSpace(raw.Prompt)
	raw.Answer = strings.TrimSpace(raw.Answer)
	raw.Explanation = strings.TrimSpace(raw.Explanation)

	options := make([]string, 0, len(raw.Options))
	for _, opt := range raw.Options {
		opt = strings.TrimSpace(opt)
		if opt != "" {
			options = append(options, opt)
		}
	}

	if raw.Topic == "" {
		raw.Topic = "DevOps"
	}
	if raw.Level == "" {
		raw.Level = "Senior"
	}
	if len(options) == 0 || raw.Prompt == "" || raw.Answer == "" {
		return questions.Question{}, errors.New("incomplete question payload from OpenAI")
	}

	if !contains(options, raw.Answer) {
		options = append(options, raw.Answer)
	}

	return questions.Question{
		Topic:       raw.Topic,
		Level:       raw.Level,
		Prompt:      raw.Prompt,
		Options:     options,
		Answer:      raw.Answer,
		Explanation: raw.Explanation,
	}, nil
}

func contains(slice []string, target string) bool {
	for _, v := range slice {
		if v == target {
			return true
		}
	}
	return false
}
