package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"go_devops_telegram_bot/internal/generator"
	"go_devops_telegram_bot/internal/questions"
)

const (
	defaultIntervalMinutes = 30
	defaultHealthAddr      = ":8080"
	defaultQuietStartHour  = 23
	defaultQuietEndHour    = 8
	defaultQuietTimezone   = "Asia/Dubai"
	defaultLanguageCode    = "en"

	envBotToken                  = "TELEGRAM_BOT_TOKEN"
	envOpenAIAPIKey              = "OPENAI_API_KEY"
	envOpenAIBaseURL             = "OPENAI_BASE_URL"
	envOpenAIModel               = "OPENAI_MODEL"
	envOpenAITemperature         = "OPENAI_TEMPERATURE"
	envOpenAIPromptCostPer1K     = "OPENAI_PROMPT_COST_PER_1K"
	envOpenAICompletionCostPer1K = "OPENAI_COMPLETION_COST_PER_1K"
	envIntervalMinutes           = "QUESTION_INTERVAL_MINUTES"
	envDebug                     = "TELEGRAM_BOT_DEBUG"
	envHealthAddr                = "HEALTH_ADDR"
	envQuietStartHour            = "QUIET_HOURS_START"
	envQuietEndHour              = "QUIET_HOURS_END"
	envQuietTimezone             = "QUIET_HOURS_TZ"
)

type languageOption struct {
	Code                string
	Name                string
	NextButton          string
	TopicLabel          string
	CorrectAnswerLabel  string
	WhyLabel            string
	GeneratingMessage   string
	ReadyTemplate       string
	ReadyTemplateNoCost string
	GenerationFailed    string
}

var languageOptions = map[string]languageOption{
	"en": {
		Code:                "en",
		Name:                "English",
		NextButton:          "Next question",
		TopicLabel:          "Topic",
		CorrectAnswerLabel:  "Correct answer",
		WhyLabel:            "Why",
		GeneratingMessage:   "⚙️ Generating a new question...",
		ReadyTemplate:       "✅ Question ready!\nTokens — prompt: %d, completion: %d, total: %d.\nApprox cost: $%.4f.\nVote in the poll below.",
		ReadyTemplateNoCost: "✅ Question ready!\nTokens — prompt: %d, completion: %d, total: %d.\nVote in the poll below.",
		GenerationFailed:    "❌ Couldn't generate a question right now. Please try again in a moment.",
	},
	"ru": {
		Code:                "ru",
		Name:                "Russian",
		NextButton:          "Следующий вопрос",
		TopicLabel:          "Тема",
		CorrectAnswerLabel:  "Правильный ответ",
		WhyLabel:            "Почему",
		GeneratingMessage:   "⚙️ Генерирую новый вопрос...",
		ReadyTemplate:       "✅ Вопрос готов!\nТокены — prompt: %d, completion: %d, всего: %d.\nПримерная стоимость: $%.4f.\nОцени варианты в опросе ниже.",
		ReadyTemplateNoCost: "✅ Вопрос готов!\nТокены — prompt: %d, completion: %d, всего: %d.\nОцени варианты в опросе ниже.",
		GenerationFailed:    "❌ Не удалось сгенерировать вопрос. Попробуйте чуть позже.",
	},
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	token := strings.TrimSpace(os.Getenv(envBotToken))
	if token == "" {
		log.Fatalf("missing %s environment variable", envBotToken)
	}

	interval := loadInterval()

	botAPI, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}
	if os.Getenv(envDebug) == "1" || strings.EqualFold(os.Getenv(envDebug), "true") {
		botAPI.Debug = true
	}

	quiet := loadQuietHours()
	qGenerator := loadQuestionGenerator()

	bot := NewQuestionBot(botAPI, questions.DefaultBank(), interval, qGenerator, quiet)
	log.Printf("bot %s initialized; interval %s", botAPI.Self.UserName, interval)

	if addr := healthAddr(); addr != "" {
		go serveHealth(ctx, addr)
	}

	go bot.startScheduler(ctx)
	if err := bot.consumeUpdates(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("updates loop: %v", err)
	}

	log.Println("shutdown complete")
}

func healthAddr() string {
	addr := strings.TrimSpace(os.Getenv(envHealthAddr))
	if addr == "" {
		return defaultHealthAddr
	}
	if strings.EqualFold(addr, "disabled") || addr == "-" {
		return ""
	}
	return addr
}

func loadInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envIntervalMinutes))
	if raw == "" {
		return time.Duration(defaultIntervalMinutes) * time.Minute
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("invalid %s=%q, falling back to %d minutes", envIntervalMinutes, raw, defaultIntervalMinutes)
		return time.Duration(defaultIntervalMinutes) * time.Minute
	}

	return time.Duration(value) * time.Minute
}

func loadQuestionGenerator() questionGenerator {
	apiKey := strings.TrimSpace(os.Getenv(envOpenAIAPIKey))
	if apiKey == "" {
		return nil
	}

	cfg := generator.Config{
		APIKey:  apiKey,
		BaseURL: strings.TrimSpace(os.Getenv(envOpenAIBaseURL)),
		Model:   strings.TrimSpace(os.Getenv(envOpenAIModel)),
	}

	if rawTemp := strings.TrimSpace(os.Getenv(envOpenAITemperature)); rawTemp != "" {
		if temp, err := strconv.ParseFloat(rawTemp, 32); err == nil {
			cfg.Temperature = float32(temp)
		} else {
			log.Printf("invalid %s=%q, using default temperature", envOpenAITemperature, rawTemp)
		}
	}

	if rawPrompt := strings.TrimSpace(os.Getenv(envOpenAIPromptCostPer1K)); rawPrompt != "" {
		if val, err := strconv.ParseFloat(rawPrompt, 64); err == nil && val >= 0 {
			cfg.PromptCostPer1K = val
		} else {
			log.Printf("invalid %s=%q, using default prompt cost", envOpenAIPromptCostPer1K, rawPrompt)
		}
	}

	if rawCompletion := strings.TrimSpace(os.Getenv(envOpenAICompletionCostPer1K)); rawCompletion != "" {
		if val, err := strconv.ParseFloat(rawCompletion, 64); err == nil && val >= 0 {
			cfg.CompletionCostPer1K = val
		} else {
			log.Printf("invalid %s=%q, using default completion cost", envOpenAICompletionCostPer1K, rawCompletion)
		}
	}

	gen, err := generator.NewOpenAIGenerator(cfg)
	if err != nil {
		log.Printf("openai generator disabled: %v", err)
		return nil
	}

	log.Printf("openai generator enabled with model %s (base %s)", cfg.Model, cfg.BaseURL)
	return gen
}

// QuestionBot handles Telegram updates and broadcasts interview questions.
type QuestionBot struct {
	api        *tgbotapi.BotAPI
	bank       *questions.Bank
	interval   time.Duration
	generator  questionGenerator
	subscriber struct {
		mu    sync.RWMutex
		chats map[int64]struct{}
	}
	language struct {
		mu      sync.RWMutex
		perChat map[int64]string
	}
	quiet quietWindow
	stats usageTracker
}

type questionGenerator interface {
	Generate(ctx context.Context, topic, language string) (generator.Result, error)
}

// NewQuestionBot constructs a bot instance.
func NewQuestionBot(api *tgbotapi.BotAPI, bank *questions.Bank, interval time.Duration, generator questionGenerator, quiet quietWindow) *QuestionBot {
	b := &QuestionBot{
		api:       api,
		bank:      bank,
		interval:  interval,
		generator: generator,
		quiet:     quiet,
	}
	b.subscriber.chats = make(map[int64]struct{})
	b.language.perChat = make(map[int64]string)
	return b
}

func (b *QuestionBot) consumeUpdates(ctx context.Context) error {
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30

	updates := b.api.GetUpdatesChan(updateConfig)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return errors.New("updates channel closed")
			}
			if update.CallbackQuery != nil {
				b.handleCallback(update.CallbackQuery)
				continue
			}
			if update.Message == nil {
				continue
			}
			if update.Message.IsCommand() {
				b.handleCommand(update.Message)
			} else {
				b.handleMessage(update.Message)
			}
		}
	}
}

func (b *QuestionBot) handleCommand(msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start", "subscribe":
		new := b.addSubscriber(msg.Chat.ID)
		intro := "You will now receive DevOps interview questions every " + b.interval.String() + "."
		if msg.Command() == "start" {
			intro = "Welcome! " + intro + " Use /question to get one instantly or /topics to see available areas."
		}
		if !new {
			intro = "You are already subscribed. Use /question for an on-demand quiz."
		}
		b.replyPlain(msg.Chat.ID, intro)
	case "unsubscribe", "stop":
		if b.removeSubscriber(msg.Chat.ID) {
			b.replyPlain(msg.Chat.ID, "Subscription removed. Use /subscribe to resume the schedule.")
		} else {
			b.replyPlain(msg.Chat.ID, "You were not subscribed. Use /subscribe to join the 30-minute quiz rotation.")
		}
	case "question":
		topic := strings.TrimSpace(msg.CommandArguments())
		if err := b.sendQuestion(msg.Chat.ID, topic); err != nil {
			log.Printf("/question failed for chat %d: %v", msg.Chat.ID, err)
		}
	case "topics":
		topics := b.bank.Topics()
		b.replyPlain(msg.Chat.ID, "Available topics:\n- "+strings.Join(topics, "\n- "))
	case "language":
		arg := strings.TrimSpace(msg.CommandArguments())
		current := b.languageForChat(msg.Chat.ID)
		if arg == "" {
			b.replyPlain(msg.Chat.ID, languageListMessage(current))
			return
		}
		if opt, ok := findLanguageOption(arg); ok {
			if opt.Code == current.Code {
				b.replyPlain(msg.Chat.ID, fmt.Sprintf("Language already set to %s (%s).", opt.Name, opt.Code))
				return
			}
			b.setLanguage(msg.Chat.ID, opt.Code)
			b.replyPlain(msg.Chat.ID, fmt.Sprintf("Language set to %s. Future questions will arrive in %s.", opt.Name, opt.Name))
			return
		}
		b.replyPlain(msg.Chat.ID, fmt.Sprintf("Unknown language %q.\n%s", arg, languageListMessage(current)))
	case "stats":
		b.sendUsageStats(msg.Chat.ID)
	case "help":
		b.replyPlain(msg.Chat.ID, helpMessage(b.interval))
	default:
		b.replyPlain(msg.Chat.ID, "Unknown command. Try /question, /topics, /stats, or /help.")
	}
}

func (b *QuestionBot) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	if err := b.sendQuestion(msg.Chat.ID, text); err != nil {
		log.Printf("direct question request failed for chat %d: %v", msg.Chat.ID, err)
	}
}

func (b *QuestionBot) handleCallback(query *tgbotapi.CallbackQuery) {
	if query == nil || query.Data == "" {
		return
	}

	if !strings.HasPrefix(query.Data, callbackNextPrefix) {
		if _, err := b.api.Request(tgbotapi.NewCallback(query.ID, "")); err != nil {
			log.Printf("answer callback failed: %v", err)
		}
		return
	}

	topic := decodeTopicFromCallback(query.Data)
	if query.Message == nil || query.Message.Chat == nil {
		if _, err := b.api.Request(tgbotapi.NewCallback(query.ID, "")); err != nil {
			log.Printf("answer callback failed: %v", err)
		}
		return
	}

	if _, err := b.api.Request(tgbotapi.NewCallback(query.ID, "")); err != nil {
		log.Printf("answer callback failed: %v", err)
	}

	if err := b.sendQuestion(query.Message.Chat.ID, topic); err != nil {
		log.Printf("callback question failed for chat %d: %v", query.Message.Chat.ID, err)
	}
}

func (b *QuestionBot) sendQuestion(chatID int64, topic string) error {
	topic = strings.TrimSpace(topic)
	lang := b.languageForChat(chatID)

	if b.generator == nil {
		errMsg := "OpenAI generator is not configured. Set OPENAI_API_KEY."
		if lang.Code == "ru" {
			errMsg = "Генератор OpenAI не настроен. Установите переменную OPENAI_API_KEY."
		}
		b.replyPlain(chatID, errMsg)
		return errors.New("openai generator unavailable")
	}

	b.sendChatAction(chatID)

	statusMsg, statusErr := b.api.Send(tgbotapi.NewMessage(chatID, lang.GeneratingMessage))

	res, err := b.generator.Generate(context.Background(), topic, lang.Name)
	if err != nil {
		log.Printf("generate question failed: %v", err)
		if statusErr == nil {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, lang.GenerationFailed))
		} else {
			b.replyPlain(chatID, lang.GenerationFailed)
		}
		return err
	}

	if err := b.deliverPoll(chatID, res.Question, lang, topic, &res); err != nil {
		log.Printf("send poll failed: %v", err)
		if statusErr == nil {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, lang.GenerationFailed))
		} else {
			b.replyPlain(chatID, lang.GenerationFailed)
		}
		return err
	}

	b.stats.record(&res)

	if statusErr == nil {
		readyText := formatReadyMessage(lang, &res)
		_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, readyText))
	}

	return nil
}

func (b *QuestionBot) deliverPoll(chatID int64, q questions.Question, lang languageOption, topic string, res *generator.Result) error {
	poll := buildPoll(chatID, q, lang, topic, res)
	if poll == nil {
		return errors.New("unable to build poll")
	}

	if _, err := b.api.Send(poll); err != nil {
		return err
	}

	cost := 0.0
	if res != nil {
		cost = res.CostUSD
	}
	log.Printf("question sent to chat %d | topic=%s | answer=%s | cost=$%.4f", chatID, q.Topic, q.Answer, cost)
	return nil
}

func (b *QuestionBot) replyPlain(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("send message failed: %v", err)
	}
}

func (b *QuestionBot) sendChatAction(chatID int64) {
	if _, err := b.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)); err != nil {
		log.Printf("send chat action failed: %v", err)
	}
}

func (b *QuestionBot) startScheduler(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if b.quiet.shouldSuppress(time.Now()) {
				log.Printf("quiet hours active (%s); skipping broadcast", b.quiet.describeRange())
				continue
			}
			chatIDs := b.subscribers()
			if len(chatIDs) == 0 {
				continue
			}

			for _, chatID := range chatIDs {
				if err := b.sendQuestion(chatID, ""); err != nil {
					log.Printf("broadcast to %d failed: %v", chatID, err)
				}
				// Avoid hitting rate limits.
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
}

func (b *QuestionBot) addSubscriber(chatID int64) bool {
	b.subscriber.mu.Lock()
	defer b.subscriber.mu.Unlock()

	if _, exists := b.subscriber.chats[chatID]; exists {
		return false
	}
	b.subscriber.chats[chatID] = struct{}{}
	return true
}

func (b *QuestionBot) removeSubscriber(chatID int64) bool {
	b.subscriber.mu.Lock()
	defer b.subscriber.mu.Unlock()

	if _, exists := b.subscriber.chats[chatID]; !exists {
		return false
	}
	delete(b.subscriber.chats, chatID)
	return true
}

func (b *QuestionBot) subscribers() []int64 {
	b.subscriber.mu.RLock()
	defer b.subscriber.mu.RUnlock()

	out := make([]int64, 0, len(b.subscriber.chats))
	for chatID := range b.subscriber.chats {
		out = append(out, chatID)
	}
	return out
}

const (
	pollQuestionMaxLen    = 280
	pollExplanationMaxLen = 190
	pollOptionMaxLen      = 90

	callbackNextPrefix = "next|"
	maxCallbackTopic   = 48
)

func nextButtonMarkup(topic string, lang languageOption) tgbotapi.InlineKeyboardMarkup {
	data := callbackNextPrefix + encodeTopicForCallback(topic)
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(lang.NextButton, data),
		),
	)
}

func encodeTopicForCallback(topic string) string {
	topic = strings.ToLower(strings.TrimSpace(topic))
	topic = strings.ReplaceAll(topic, " ", "_")
	if utf8.RuneCountInString(topic) > maxCallbackTopic {
		topic = truncateRunes(topic, maxCallbackTopic)
	}
	return topic
}

func decodeTopicFromCallback(data string) string {
	if !strings.HasPrefix(data, callbackNextPrefix) {
		return ""
	}
	topic := strings.TrimPrefix(data, callbackNextPrefix)
	topic = strings.ReplaceAll(topic, "_", " ")
	return strings.TrimSpace(topic)
}

func (b *QuestionBot) languageForChat(chatID int64) languageOption {
	b.language.mu.RLock()
	code, ok := b.language.perChat[chatID]
	b.language.mu.RUnlock()
	if !ok {
		return languageOptions[defaultLanguageCode]
	}
	if opt, exists := languageOptions[code]; exists {
		return opt
	}
	return languageOptions[defaultLanguageCode]
}

func (b *QuestionBot) setLanguage(chatID int64, code string) {
	if _, exists := languageOptions[code]; !exists {
		return
	}
	b.language.mu.Lock()
	b.language.perChat[chatID] = code
	b.language.mu.Unlock()
}

func findLanguageOption(input string) (languageOption, bool) {
	if input == "" {
		return languageOptions[defaultLanguageCode], true
	}
	key := strings.ToLower(strings.TrimSpace(input))
	if opt, exists := languageOptions[key]; exists {
		return opt, true
	}
	for _, opt := range languageOptions {
		if strings.ToLower(opt.Name) == key {
			return opt, true
		}
	}
	return languageOptions[defaultLanguageCode], false
}

func languageListMessage(current languageOption) string {
	var rows []string
	for code, opt := range languageOptions {
		rows = append(rows, fmt.Sprintf("%s - %s", code, opt.Name))
	}
	slices.Sort(rows)
	return fmt.Sprintf("Available languages:\n%s\nCurrent: %s (%s).\nUse /language <code> to switch.", strings.Join(rows, "\n"), current.Name, current.Code)
}

type usageTracker struct {
	mu                 sync.Mutex
	totalQuestions     int
	generatedQuestions int
	promptTokens       int
	completionTokens   int
	costUSD            float64
}

type usageSnapshot struct {
	totalQuestions     int
	generatedQuestions int
	promptTokens       int
	completionTokens   int
	costUSD            float64
}

func (u *usageTracker) record(res *generator.Result) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.totalQuestions++
	if res == nil {
		return
	}

	u.generatedQuestions++
	u.promptTokens += res.PromptTokens
	u.completionTokens += res.CompletionTokens
	u.costUSD += res.CostUSD
}

func (u *usageTracker) snapshot() usageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()

	return usageSnapshot{
		totalQuestions:     u.totalQuestions,
		generatedQuestions: u.generatedQuestions,
		promptTokens:       u.promptTokens,
		completionTokens:   u.completionTokens,
		costUSD:            u.costUSD,
	}
}

func (b *QuestionBot) sendUsageStats(chatID int64) {
	snap := b.stats.snapshot()

	local := snap.totalQuestions - snap.generatedQuestions
	lines := []string{
		fmt.Sprintf("Questions sent: %d (GPT-5: %d, local: %d)", snap.totalQuestions, snap.generatedQuestions, local),
		fmt.Sprintf("Prompt tokens: %d", snap.promptTokens),
		fmt.Sprintf("Completion tokens: %d", snap.completionTokens),
		fmt.Sprintf("Approximate cost: $%.4f", snap.costUSD),
	}

	if snap.generatedQuestions > 0 && snap.costUSD > 0 {
		avg := snap.costUSD / float64(snap.generatedQuestions)
		lines = append(lines, fmt.Sprintf("Avg cost per generated question: $%.4f", avg))
	}

	lines = append(lines, fmt.Sprintf("Preferred language: %s", b.languageForChat(chatID).Name))

	msg := tgbotapi.NewMessage(chatID, strings.Join(lines, "\n"))
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("send stats failed: %v", err)
	}
}

func formatReadyMessage(lang languageOption, res *generator.Result) string {
	if res == nil {
		return lang.GenerationFailed
	}

	if res.CostUSD > 0 {
		return fmt.Sprintf(lang.ReadyTemplate, res.PromptTokens, res.CompletionTokens, res.TotalTokens, res.CostUSD)
	}

	return fmt.Sprintf(lang.ReadyTemplateNoCost, res.PromptTokens, res.CompletionTokens, res.TotalTokens)
}

func buildPoll(chatID int64, q questions.Question, lang languageOption, topic string, result *generator.Result) *tgbotapi.SendPollConfig {
	if len(q.Options) < 2 {
		return nil
	}

	question := buildPollQuestion(q, lang)
	options := make([]string, len(q.Options))
	correctIdx := -1
	for i, opt := range q.Options {
		trimmed := strings.TrimSpace(opt)
		if correctIdx == -1 && trimmed == strings.TrimSpace(q.Answer) {
			correctIdx = i
		}
		options[i] = truncateRunes(trimmed, pollOptionMaxLen)
	}

	if correctIdx == -1 {
		truncatedAnswer := truncateRunes(strings.TrimSpace(q.Answer), pollOptionMaxLen)
		for i, opt := range options {
			if opt == truncatedAnswer {
				correctIdx = i
				break
			}
		}
	}
	if correctIdx < 0 {
		return nil
	}

	poll := tgbotapi.NewPoll(chatID, question, options...)
	poll.Type = "quiz"
	poll.IsAnonymous = false
	poll.CorrectOptionID = int64(correctIdx)

	explanation := buildPollExplanation(q, lang, result)
	if explanation != "" {
		poll.Explanation = explanation
		poll.ExplanationParseMode = "MarkdownV2"
	}

	poll.ReplyMarkup = nextButtonMarkup(topic, lang)

	return &poll
}

func buildPollQuestion(q questions.Question, lang languageOption) string {
	header := q.Prompt
	header = strings.TrimSpace(header)
	if header == "" {
		header = "Select the correct answer"
	}

	prefix := strings.TrimSpace(q.Topic)
	level := strings.TrimSpace(q.Level)
	if prefix != "" && level != "" {
		prefix = fmt.Sprintf("%s (%s)", prefix, level)
	} else if level != "" {
		prefix = level
	}

	if prefix != "" {
		header = fmt.Sprintf("%s: %s — %s", lang.TopicLabel, prefix, header)
	}

	return truncateRunes(header, pollQuestionMaxLen)
}

func buildPollExplanation(q questions.Question, lang languageOption, result *generator.Result) string {
	content := []string{
		lang.CorrectAnswerLabel + ": " + q.Answer,
	}
	if strings.TrimSpace(q.Explanation) != "" {
		content = append(content, lang.WhyLabel+": "+q.Explanation)
	}

	if result != nil && (result.PromptTokens > 0 || result.CompletionTokens > 0) {
		content = append(content, fmt.Sprintf("Tokens: %d/%d/%d", result.PromptTokens, result.CompletionTokens, result.TotalTokens))
		if result.CostUSD > 0 {
			content = append(content, fmt.Sprintf("Cost: $%.4f", result.CostUSD))
		}
	}

	body := escapeMarkdownV2(strings.Join(content, "\n"))
	if body == "" {
		return ""
	}

	if utf8.RuneCountInString(body) > pollExplanationMaxLen-4 {
		body = truncateRunes(body, pollExplanationMaxLen-4)
	}

	return "||" + body + "||"
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	runes = runes[:limit]
	// Avoid leaving a dangling escape character that would break MarkdownV2.
	if len(runes) > 0 && runes[len(runes)-1] == '\\' {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func optionLetter(idx int) string {
	return string(rune('A' + idx))
}

func escapeMarkdownV2(input string) string {
	replacer := strings.NewReplacer(
		`_`, `\_`,
		`*`, `\*`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
		`~`, `\~`,
		`>`, `\>`,
		`#`, `\#`,
		`+`, `\+`,
		`-`, `\-`,
		`=`, `\=`,
		`|`, `\|`,
		`{`, `\{`,
		`}`, `\}`,
		`.`, `\.`,
		`!`, `\!`,
	)
	return replacer.Replace(input)
}

func helpMessage(interval time.Duration) string {
	return strings.Join([]string{
		"Use /question to get a random DevOps interview question immediately.",
		"Provide a topic like `/question kubernetes` to scope the quiz.",
		"Use /topics to list all available areas.",
		"Use /subscribe to receive a new question every " + interval.String() + ".",
		"Use /unsubscribe to stop the schedule.",
		"Answers arrive as Telegram spoilers so you can self-assess first.",
		"Use /stats to see token usage and cost summaries since startup.",
		"Use /language to change the language used for generated questions.",
	}, "\n")
}

func serveHealth(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("healthz write: %v", err)
		}
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health server shutdown: %v", err)
		}
	}()

	log.Printf("health endpoint listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("health server error: %v", err)
	}
}

type quietWindow struct {
	enabled  bool
	start    int
	end      int
	location *time.Location
}

func (q quietWindow) shouldSuppress(t time.Time) bool {
	if !q.enabled || q.location == nil {
		return false
	}
	local := t.In(q.location)
	hour := local.Hour()

	if q.start == q.end {
		return false
	}
	if q.start < q.end {
		return hour >= q.start && hour < q.end
	}
	return hour >= q.start || hour < q.end
}

func (q quietWindow) describeRange() string {
	if !q.enabled || q.location == nil {
		return "disabled"
	}
	name := q.location.String()
	return fmt.Sprintf("%02d:00-%02d:00 %s", q.start, q.end, name)
}

func (q quietWindow) locationName() string {
	if q.location == nil {
		return ""
	}
	return q.location.String()
}

func loadQuietHours() quietWindow {
	startRaw := strings.TrimSpace(os.Getenv(envQuietStartHour))
	endRaw := strings.TrimSpace(os.Getenv(envQuietEndHour))

	if disableQuiet(startRaw) || disableQuiet(endRaw) {
		return quietWindow{}
	}

	start := defaultQuietStartHour
	if startRaw != "" {
		if parsed, err := strconv.Atoi(startRaw); err == nil && parsed >= 0 && parsed <= 23 {
			start = parsed
		} else {
			log.Printf("invalid %s=%q, using default %d", envQuietStartHour, startRaw, defaultQuietStartHour)
		}
	}

	end := defaultQuietEndHour
	if endRaw != "" {
		if parsed, err := strconv.Atoi(endRaw); err == nil && parsed >= 0 && parsed <= 23 {
			end = parsed
		} else {
			log.Printf("invalid %s=%q, using default %d", envQuietEndHour, endRaw, defaultQuietEndHour)
		}
	}

	tz := strings.TrimSpace(os.Getenv(envQuietTimezone))
	if tz == "" {
		tz = defaultQuietTimezone
	}

	location, err := time.LoadLocation(tz)
	if err != nil {
		log.Printf("load timezone %q failed (%v), falling back to UTC", tz, err)
		location = time.UTC
	}

	window := quietWindow{
		enabled:  start != end,
		start:    start,
		end:      end,
		location: location,
	}

	log.Printf("quiet hours configured: %s", window.describeRange())
	return window
}

func disableQuiet(value string) bool {
	return strings.EqualFold(value, "disabled") || strings.EqualFold(value, "-")
}
