package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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

	envBotToken          = "TELEGRAM_BOT_TOKEN"
	envOpenAIAPIKey      = "OPENAI_API_KEY"
	envOpenAIBaseURL     = "OPENAI_BASE_URL"
	envOpenAIModel       = "OPENAI_MODEL"
	envOpenAITemperature = "OPENAI_TEMPERATURE"
	envIntervalMinutes   = "QUESTION_INTERVAL_MINUTES"
	envDebug             = "TELEGRAM_BOT_DEBUG"
	envHealthAddr        = "HEALTH_ADDR"
	envQuietStartHour    = "QUIET_HOURS_START"
	envQuietEndHour      = "QUIET_HOURS_END"
	envQuietTimezone     = "QUIET_HOURS_TZ"
)

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
	history struct {
		mu      sync.RWMutex
		perChat map[int64]map[int]struct{}
	}
	quiet quietWindow
}

type questionGenerator interface {
	Generate(ctx context.Context, topic string) (questions.Question, error)
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
	b.history.perChat = make(map[int64]map[int]struct{})
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
			b.replyPlain(msg.Chat.ID, fmt.Sprintf("Could not fetch a question: %v", err))
		}
	case "topics":
		topics := b.bank.Topics()
		b.replyPlain(msg.Chat.ID, "Available topics:\n- "+strings.Join(topics, "\n- "))
	case "help":
		b.replyPlain(msg.Chat.ID, helpMessage(b.interval))
	default:
		b.replyPlain(msg.Chat.ID, "Unknown command. Try /question, /topics, or /help.")
	}
}

func (b *QuestionBot) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	if err := b.sendQuestion(msg.Chat.ID, text); err != nil {
		b.replyPlain(msg.Chat.ID, fmt.Sprintf("No topic match for %q. Use /topics to see options or /question for random.", text))
	}
}

func (b *QuestionBot) sendQuestion(chatID int64, topic string) error {
	if b.generator != nil {
		q, err := b.generator.Generate(context.Background(), topic)
		if err == nil {
			return b.deliverQuestion(chatID, q)
		}
		log.Printf("OpenAI generator failed, falling back to local bank: %v", err)
	}
	return b.sendFromBank(chatID, topic)
}

func (b *QuestionBot) sendFromBank(chatID int64, topic string) error {
	exclude := b.historySnapshot(chatID)
	q, idx, reset, err := b.bank.RandomWithExclusion(topic, exclude)
	if err != nil {
		return err
	}

	if err := b.deliverQuestion(chatID, q); err != nil {
		return err
	}

	b.rememberQuestion(chatID, idx, reset)
	return nil
}

func (b *QuestionBot) deliverQuestion(chatID int64, q questions.Question) error {
	body := buildQuestionMessage(q)
	msg := tgbotapi.NewMessage(chatID, body)
	msg.DisableWebPagePreview = true

	if _, err := b.api.Send(msg); err != nil {
		return err
	}

	answer := buildSpoilerAnswer(q)
	answerMsg := tgbotapi.NewMessage(chatID, answer)
	answerMsg.ParseMode = "MarkdownV2"
	answerMsg.DisableWebPagePreview = true

	if _, err := b.api.Send(answerMsg); err != nil {
		return err
	}

	return nil
}

func (b *QuestionBot) replyPlain(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("send message failed: %v", err)
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

func buildQuestionMessage(q questions.Question) string {
	var sb strings.Builder
	sb.WriteString("Topic: ")
	sb.WriteString(q.Topic)
	if q.Level != "" {
		sb.WriteString(" (")
		sb.WriteString(q.Level)
		sb.WriteString(")")
	}
	sb.WriteString("\n\n")
	sb.WriteString(q.Prompt)
	sb.WriteString("\n\n")

	for idx, opt := range q.Options {
		sb.WriteString(fmt.Sprintf("%s. %s\n", optionLetter(idx), opt))
	}

	sb.WriteString("\nAnswer hidden below. Tap the spoiler to reveal.")
	return sb.String()
}

func buildSpoilerAnswer(q questions.Question) string {
	details := []string{
		"Correct answer: " + q.Answer,
	}
	if strings.TrimSpace(q.Explanation) != "" {
		details = append(details, "Why: "+q.Explanation)
	}

	body := strings.Join(details, "\n")
	return fmt.Sprintf("||%s||", escapeMarkdownV2(body))
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
	}, "\n")
}

func (b *QuestionBot) historySnapshot(chatID int64) map[int]struct{} {
	b.history.mu.RLock()
	defer b.history.mu.RUnlock()

	if len(b.history.perChat) == 0 {
		return nil
	}

	stored, ok := b.history.perChat[chatID]
	if !ok || len(stored) == 0 {
		return nil
	}

	copy := make(map[int]struct{}, len(stored))
	for idx := range stored {
		copy[idx] = struct{}{}
	}
	return copy
}

func (b *QuestionBot) rememberQuestion(chatID int64, idx int, reset bool) {
	b.history.mu.Lock()
	defer b.history.mu.Unlock()

	if b.history.perChat == nil {
		b.history.perChat = make(map[int64]map[int]struct{})
	}

	if reset {
		b.history.perChat[chatID] = map[int]struct{}{
			idx: {},
		}
		return
	}

	hist := b.history.perChat[chatID]
	if hist == nil {
		hist = make(map[int]struct{})
		b.history.perChat[chatID] = hist
	}

	hist[idx] = struct{}{}
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
