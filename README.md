# DevOps Telegram Interview Bot

Go-based Telegram bot that quizzes you with mid-to-senior DevOps interview questions. It supports an automatic schedule (every 30 minutes by default), on-demand questions, topic filtering, and spoiler-protected answers so you can self-assess.

## Features
- Telegram quiz polls with spoilered explanations so you can answer interactively and reveal feedback in the same message.
- Dynamic questions via OpenAI (set `OPENAI_API_KEY`) with automatic fallback to a curated on-disk bank covering Ansible, Docker, Linux, Kubernetes, GitLab CI, Bash, Python, Nginx, HAProxy, Grafana, Prometheus, ELK, SQL, ClickHouse, and general DevOps practices.
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
- `/help` &mdash; recap controls.

## Customisation
- **Change the schedule**: set `QUESTION_INTERVAL_MINUTES` to any positive integer.
- **Extend the question bank**: edit `internal/questions/questions.go` and append to `defaultQuestions`.
- **Integrate an external generator**: replace `questions.DefaultBank()` in `cmd/bot/main.go` with a provider that calls an API such as OpenAI, while preserving the `Question` struct contract.
- **Health endpoint**: override `HEALTH_ADDR` (default `:8080`); set to `disabled` or `-` to turn it off.
- **Quiet hours**: override `QUIET_HOURS_START`, `QUIET_HOURS_END`, and `QUIET_HOURS_TZ` (defaults 23–08 in Asia/Dubai). Set either start or end to `disabled`/`-` to turn the feature off.
- **OpenAI generation**: set `OPENAI_API_KEY` (required). Optional: `OPENAI_MODEL` (default `gpt-5`), `OPENAI_BASE_URL`, and `OPENAI_TEMPERATURE` (default 1; some GPT-5 variants only accept the default). If the API call fails, the bot automatically uses the local bank.

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
