# DevOps Telegram Interview Bot

Go-based Telegram bot that quizzes you with mid-to-senior DevOps interview questions. It supports an automatic schedule (every 30 minutes by default), on-demand questions, topic filtering, and spoiler-protected answers so you can self-assess.

## Features
- Telegram quiz polls with spoilered explanations so you can answer interactively and reveal feedback in the same message.
- Inline "Next question" button to queue a fresh quiz without typing commands.
- Token usage and cost estimates per generated question, plus `/stats` to see totals since startup.
- Telegram typing indicator while questions are being generated so you can see progress.
- Per-chat language selection (`/language`) for English and Russian delivery.
- GPT-5 explanations read like mini-lessons with step-by-step reasoning and practical takeaways for deeper mastery.
- Dynamic questions are always generated via OpenAI (set `OPENAI_API_KEY`) across Ansible, Docker, Linux, Kubernetes, GitLab CI, Bash, Python, Nginx, HAProxy, Grafana, Prometheus, ELK, SQL, ClickHouse, and general DevOps practices.
- `/question` command for instant quizzes, optionally filtered by topic (e.g. `/question kubernetes`).
- `/subscribe` (or `/start`) to opt-in to the automatic 30-minute rotation; `/unsubscribe` stops it.
- Answers arrive as Telegram spoilers (`||text||`) to avoid accidental reveals.
- Per-chat history ensures you do not see repeat questions until the pool is exhausted.
- Built-in `/healthz` endpoint (default `:8080`) for container liveness probes.
- Scheduler observes quiet hours (default 23:00–08:00 Asia/Dubai) to avoid late-night pings.

## Prerequisites
1. Go 1.22 or newer.
2. A Telegram bot token from [@BotFather](https://t.me/BotFather).

## Quick start
```bash
cp .env.example .env  # optional helper file
export TELEGRAM_BOT_TOKEN="<your-telegram-token>"
# Optional: enable OpenAI generation (falls back to local bank if unavailable)
# export OPENAI_API_KEY="sk-..."
# export OPENAI_MODEL="gpt-5"
# export OPENAI_TEMPERATURE=1
# Optional: adjust cost assumptions (USD per 1K tokens)
# export OPENAI_PROMPT_COST_PER_1K=0.01
# export OPENAI_COMPLETION_COST_PER_1K=0.03
# Optional: change the 30 minute cadence
# export QUESTION_INTERVAL_MINUTES=45
# Optional: change or disable the health endpoint
# export HEALTH_ADDR=":9090"   # set to "-" or "disabled" to turn it off
# Optional: adjust quiet hours (24h clock) or timezone
# export QUIET_HOURS_START=23
# export QUIET_HOURS_END=8
# export QUIET_HOURS_TZ="Asia/Dubai"  # set start/end to "disabled" to switch off

go run ./cmd/bot
```

Inside Telegram, use `/language` to see supported languages or `/language ru` to switch.

Deploy the binary with the same environment variables:
```bash
go build -o devops-bot ./cmd/bot
./devops-bot
```

## Commands
- `/start` or `/subscribe` &mdash; subscribe the current chat to the scheduled questions.
- `/unsubscribe` or `/stop` &mdash; opt out from the schedule.
- `/question [topic]` &mdash; send a question immediately; include a topic keyword to filter.
- `/topics` &mdash; list all available subjects in the built-in bank.
- `/stats` &mdash; view token usage and approximate OpenAI cost totals since startup.
- `/language [code]` &mdash; change the language used for generated questions (run without arguments to see the supported list).
- `/help` &mdash; recap controls.

## Customisation
- **Change the schedule**: set `QUESTION_INTERVAL_MINUTES` to any positive integer.
- **Extend the question bank**: edit `internal/questions/questions.go` and append to `defaultQuestions`.
- **Integrate an external generator**: replace `questions.DefaultBank()` in `cmd/bot/main.go` with a provider that calls an API such as OpenAI, while preserving the `Question` struct contract.
- **Health endpoint**: override `HEALTH_ADDR` (default `:8080`); set to `disabled` or `-` to turn it off.
- **Quiet hours**: override `QUIET_HOURS_START`, `QUIET_HOURS_END`, and `QUIET_HOURS_TZ` (defaults 23–08 in Asia/Dubai). Set either start or end to `disabled`/`-` to turn the feature off.
- **OpenAI generation**: set `OPENAI_API_KEY` (required). Optional: `OPENAI_MODEL` (default `gpt-5`), `OPENAI_BASE_URL`, and `OPENAI_TEMPERATURE` (default 1; some GPT-5 variants only accept the default). If the API call fails, the bot reports the error and no question is sent.
- **Cost assumptions**: override `OPENAI_PROMPT_COST_PER_1K` and `OPENAI_COMPLETION_COST_PER_1K` (USD per 1K tokens) to keep the usage estimates aligned with your pricing.
- **Per-chat language**: run `/language` in Telegram to see the supported set or `/language ru` (for example) to switch.

## Container image
Build and run via Docker:
```bash
docker build -t devops-telegram-bot .
docker run -d \
  --name devops-bot \
  -e TELEGRAM_BOT_TOKEN="your-token" \
  -e QUESTION_INTERVAL_MINUTES=30 \
  -e QUIET_HOURS_START=23 \
  -e QUIET_HOURS_END=8 \
  -e QUIET_HOURS_TZ="Asia/Dubai" \
  -e OPENAI_API_KEY="sk-..." \
  -e OPENAI_MODEL="gpt-5" \
  -e OPENAI_PROMPT_COST_PER_1K=0.01 \
  -e OPENAI_COMPLETION_COST_PER_1K=0.03 \
  -p 8080:8080 \
  devops-telegram-bot
```

The Dockerfile provides:
- A non-root user for the bot process.
- `HEALTHCHECK` hitting `http://127.0.0.1:8080/healthz`.
- Alpine base with CA certificates for outbound HTTPS to Telegram.

## Operational notes
- Subscribers are tracked in-memory; restart the bot to reset subscriptions.
- Keep your `.env` or environment variables out of version control to protect credentials.
- The bot uses basic throttling between broadcasts to respect Telegram rate limits; adjust the 500ms pause if you serve many chats.
- Each generated question includes token counts and cost in the poll’s explanation; use `/stats` for totals.
- If the OpenAI API is unavailable, the bot reports an error so you can retry once the service recovers.
