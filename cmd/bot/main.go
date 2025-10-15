package main

import (
	"context"
	"errors"
	"fmt"
	"html"
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

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"go_devops_telegram_bot/internal/generator"
	"go_devops_telegram_bot/internal/questions"

	_ "time/tzdata"
)

const (
	defaultIntervalMinutes = 30
	defaultHealthAddr      = ":8080"
	defaultQuietStartHour  = 23
	defaultQuietEndHour    = 8
	defaultQuietTimezone   = "Asia/Dubai"
	defaultLanguageCode    = "ru"

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
	Code               string
	Name               string
	NextButton         string
	PollInstruction    string
	AnswerRevealTitle  string
	TopicLabel         string
	CorrectAnswerLabel string
	WhyLabel           string
	TokensLabel        string
	CostLabel          string
	GeneratingMessage  string
	GenerationFailed   string
}

var languageOptions = map[string]languageOption{
	"en": {
		Code:               "en",
		Name:               "English",
		NextButton:         "/question",
		PollInstruction:    "Pick the best answer",
		AnswerRevealTitle:  "Click to see the right answer",
		TopicLabel:         "Topic",
		CorrectAnswerLabel: "Correct answer",
		WhyLabel:           "Why",
		TokensLabel:        "Tokens",
		CostLabel:          "Cost",
		GeneratingMessage:  "⚙️ Generating a new question...",
		GenerationFailed:   "❌ Couldn't generate a question right now. Please try again in a moment.",
	},
	"ru": {
		Code:               "ru",
		Name:               "Russian",
		NextButton:         "/question",
		PollInstruction:    "Выберите правильный вариант",
		AnswerRevealTitle:  "Нажмите, чтобы увидеть правильный ответ",
		TopicLabel:         "Тема",
		CorrectAnswerLabel: "Правильный ответ",
		WhyLabel:           "Почему",
		TokensLabel:        "Токены",
		CostLabel:          "Стоимость",
		GeneratingMessage:  "⚙️ Генерирую новый вопрос...",
		GenerationFailed:   "❌ Не удалось сгенерировать вопрос. Попробуйте чуть позже.",
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
	quiet    quietWindow
	stats    usageTracker
	schedule struct {
		mu      sync.Mutex
		nextRun map[int64]time.Time
	}
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
	b.schedule.nextRun = make(map[int64]time.Time)
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

	if strings.HasPrefix(query.Data, callbackNextPrefix) {
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
		return
	}

	if _, err := b.api.Request(tgbotapi.NewCallback(query.ID, "")); err != nil {
		log.Printf("answer callback failed: %v", err)
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

	pollQuestion := buildPollQuestion(res.Question, lang)
	pollOptions, correctIdx := buildPollOptions(res.Question)
	if correctIdx < 0 {
		log.Printf("build poll options failed: correct answer missing")
		if statusErr == nil {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, lang.GenerationFailed))
		} else {
			b.replyPlain(chatID, lang.GenerationFailed)
		}
		return errors.New("correct answer not found in options")
	}

	poll := tgbotapi.NewPoll(chatID, pollQuestion, pollOptions...)
	poll.Type = "quiz"
	poll.IsAnonymous = false
	poll.CorrectOptionID = int64(correctIdx)

	_, err = b.api.Send(poll)
	if err != nil {
		log.Printf("send question poll failed: %v", err)
		if statusErr == nil {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, lang.GenerationFailed))
		} else {
			b.replyPlain(chatID, lang.GenerationFailed)
		}
		return err
	}

	answerMsg := tgbotapi.NewMessage(chatID, buildAnswerMessage(res, lang))
	answerMsg.ParseMode = "HTML"
	answerMsg.DisableWebPagePreview = true
	answerMsg.ReplyMarkup = nextButtonMarkup(topic, lang)

	if _, err := b.api.Send(answerMsg); err != nil {
		log.Printf("send answer message failed: %v", err)
		if statusErr == nil {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, lang.GenerationFailed))
		} else {
			b.replyPlain(chatID, lang.GenerationFailed)
		}
		return err
	}

	if statusErr == nil {
		_, _ = b.api.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
	}

	b.stats.record(&res)

	b.markQuestionSent(chatID)

	cost := res.CostUSD
	log.Printf("question sent to chat %d | topic=%s | answer=%s | cost=$%.4f", chatID, res.Question.Topic, res.Question.Answer, cost)
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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if b.quiet.shouldSuppress(now) {
				log.Printf("quiet hours active (%s); skipping broadcast", b.quiet.describeRange())
				continue
			}
			dueChats := b.dueSubscribers(now)
			if len(dueChats) == 0 {
				continue
			}

			for _, chatID := range dueChats {
				if !b.isSubscribed(chatID) {
					b.clearSchedule(chatID)
					continue
				}
				if err := b.sendQuestion(chatID, ""); err != nil {
					log.Printf("broadcast to %d failed: %v", chatID, err)
					b.scheduleNext(chatID, now.Add(1*time.Minute))
					continue
				}
				// Avoid hitting rate limits.
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
}

func (b *QuestionBot) addSubscriber(chatID int64) bool {
	b.subscriber.mu.Lock()
	if _, exists := b.subscriber.chats[chatID]; exists {
		b.subscriber.mu.Unlock()
		return false
	}
	b.subscriber.chats[chatID] = struct{}{}
	b.subscriber.mu.Unlock()

	b.scheduleNext(chatID, time.Now().Add(b.interval))
	return true
}

func (b *QuestionBot) removeSubscriber(chatID int64) bool {
	b.subscriber.mu.Lock()
	if _, exists := b.subscriber.chats[chatID]; !exists {
		b.subscriber.mu.Unlock()
		return false
	}
	delete(b.subscriber.chats, chatID)
	b.subscriber.mu.Unlock()

	b.clearSchedule(chatID)
	return true
}

const (
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
	runes := []rune(topic)
	if len(runes) > maxCallbackTopic {
		topic = string(runes[:maxCallbackTopic])
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

func (b *QuestionBot) isSubscribed(chatID int64) bool {
	b.subscriber.mu.RLock()
	defer b.subscriber.mu.RUnlock()
	_, exists := b.subscriber.chats[chatID]
	return exists
}

func (b *QuestionBot) scheduleNext(chatID int64, when time.Time) {
	b.schedule.mu.Lock()
	defer b.schedule.mu.Unlock()
	if b.schedule.nextRun == nil {
		b.schedule.nextRun = make(map[int64]time.Time)
	}
	b.schedule.nextRun[chatID] = when
}

func (b *QuestionBot) clearSchedule(chatID int64) {
	b.schedule.mu.Lock()
	defer b.schedule.mu.Unlock()
	delete(b.schedule.nextRun, chatID)
}

func (b *QuestionBot) dueSubscribers(now time.Time) []int64 {
	b.schedule.mu.Lock()
	defer b.schedule.mu.Unlock()

	due := make([]int64, 0, len(b.schedule.nextRun))
	for chatID, ts := range b.schedule.nextRun {
		if !now.Before(ts) {
			due = append(due, chatID)
		}
	}
	return due
}

func (b *QuestionBot) markQuestionSent(chatID int64) {
	if !b.isSubscribed(chatID) {
		return
	}
	b.scheduleNext(chatID, time.Now().Add(b.interval))
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

	lines := []string{
		fmt.Sprintf("Questions sent: %d", snap.totalQuestions),
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

func buildPollQuestion(q questions.Question, lang languageOption) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(lang.TopicLabel))
	sb.WriteString(": ")
	sb.WriteString(strings.TrimSpace(q.Topic))
	if level := strings.TrimSpace(q.Level); level != "" {
		sb.WriteString(" (")
		sb.WriteString(level)
		sb.WriteString(")")
	}

	prompt := sanitizeMultiline(q.Prompt)
	if prompt != "" {
		sb.WriteString("\n\n")
		sb.WriteString(prompt)
	}

	instruction := strings.TrimSpace(lang.PollInstruction)
	if instruction != "" {
		sb.WriteString("\n\n")
		sb.WriteString(instruction)
	}

	return trimRunes(sb.String(), maxPollQuestionRunes)
}

func buildPollOptions(q questions.Question) ([]string, int) {
	answer := strings.TrimSpace(q.Answer)
	options := make([]string, 0, len(q.Options))
	correctIdx := -1

	for idx, opt := range q.Options {
		if strings.TrimSpace(opt) == answer {
			correctIdx = idx
		}
		normalized := sanitizeInline(opt)
		options = append(options, trimRunes(normalized, maxPollOptionRunes))
	}

	return options, correctIdx
}

func buildAnswerMessage(res generator.Result, lang languageOption) string {
	var sb strings.Builder
	sb.WriteString("<b>")
	sb.WriteString(html.EscapeString(lang.AnswerRevealTitle))
	sb.WriteString("</b>\n")
	sb.WriteString("<span class=\"tg-spoiler\"><b>")
	sb.WriteString(html.EscapeString(lang.CorrectAnswerLabel))
	sb.WriteString(":</b> ")
	sb.WriteString(html.EscapeString(strings.TrimSpace(res.Question.Answer)))

	if explanation := strings.TrimSpace(res.Question.Explanation); explanation != "" {
		sb.WriteString("\n<b>")
		sb.WriteString(html.EscapeString(lang.WhyLabel))
		sb.WriteString(":</b> ")
		sb.WriteString(htmlize(explanation))
	}

	if res.PromptTokens > 0 || res.CompletionTokens > 0 {
		sb.WriteString("\n\n<b>")
		sb.WriteString(html.EscapeString(lang.TokensLabel))
		sb.WriteString(":</b> ")
		sb.WriteString(fmt.Sprintf("%d/%d/%d", res.PromptTokens, res.CompletionTokens, res.TotalTokens))
		if res.CostUSD > 0 {
			sb.WriteString("\n<b>")
			sb.WriteString(html.EscapeString(lang.CostLabel))
			sb.WriteString(":</b> $")
			sb.WriteString(fmt.Sprintf("%.4f", res.CostUSD))
		}
	}

	sb.WriteString("</span>")
	return sb.String()
}

func htmlize(text string) string {
	return html.EscapeString(strings.TrimSpace(text))
}

const (
	maxPollQuestionRunes = 280
	maxPollOptionRunes   = 90
)

func trimRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return string(runes[0])
	}
	trimmed := strings.TrimSpace(string(runes[:limit-1]))
	return trimmed + "…"
}

func sanitizeMultiline(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func sanitizeInline(text string) string {
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func helpMessage(interval time.Duration) string {
	return strings.Join([]string{
		"Use /question to get a random DevOps interview question immediately.",
		"Provide a topic like `/question kubernetes` to scope the quiz.",
		"Use /topics to list all available areas.",
		"Use /subscribe to receive a new question every " + interval.String() + ".",
		"Use /unsubscribe to stop the schedule.",
		"Questions arrive as polls; open the spoiler message underneath to review the answer and explanation.",
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
	tz := strings.TrimSpace(os.Getenv(envQuietTimezone))
	if tz == "" {
		tz = defaultQuietTimezone
	}

	location, err := time.LoadLocation(tz)
	if err != nil {
		log.Printf("load timezone %q failed (%v), falling back to UTC", tz, err)
		location = time.UTC
	}

	// Align the process-wide local timezone with the configured quiet hours timezone so that logs
	// and other time helpers reflect the expected local wall clock.
	time.Local = location

	if disableQuiet(startRaw) || disableQuiet(endRaw) {
		log.Printf("quiet hours disabled; timezone set to %s", location.String())
		return quietWindow{location: location}
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

	window := quietWindow{
		enabled:  start != end,
		start:    start,
		end:      end,
		location: location,
	}

	log.Printf("quiet hours configured: %s (log timezone %s)", window.describeRange(), location.String())
	return window
}

func disableQuiet(value string) bool {
	return strings.EqualFold(value, "disabled") || strings.EqualFold(value, "-")
}
