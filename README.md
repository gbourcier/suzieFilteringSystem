# digestd

`digestd` reads one IMAP mailbox, archives every original message, summarizes
in-scope mail with a local Ollama model, and sends one weekly digest.

Message contents stay on the host. The service never deletes, moves, replies
to, or forwards source mail. It marks each new message as read only after the
message has been archived and persisted.

## Quickstart

Requirements:

- Docker Engine with Compose
- An IMAP mailbox over TLS
- An SMTP account supporting STARTTLS
- About 9 GB for `qwen2.5:14b`, plus the persistent archive

Configure and start the services:

```sh
cp .env.example .env
# Edit .env with the real IMAP and SMTP credentials.
docker compose up -d ollama
make pull-model
docker compose up -d digestd
docker compose logs -f digestd
```

Pull the model before starting `digestd`. On first startup, the service records
the folder's current high-water UID without fetching or changing existing mail.
Only messages arriving after that baseline are processed and marked read.
Weekly digests only run on `DIGEST_SCHEDULE`; startup never sends one
off-schedule.

## Persistent Data

The `digestd-data` volume contains:

- `/data/digestd.db`: SQLite state and summaries
- `/data/archive/YYYY/MM/*.eml`: immutable original messages

Back up and restore the entire volume as one unit so database paths continue to
match the archived files. Stop `digestd` before taking a filesystem-level copy:

```sh
docker compose stop digestd
docker run --rm \
  -v digestd-data:/data:ro \
  -v "$PWD":/backup \
  alpine tar -czf /backup/digestd-data.tgz -C /data .
docker compose start digestd
```

To restore, create an empty `digestd-data` volume and extract the archive into
its root before starting the service.

## Operations

The first model pull is roughly 9 GB. Processing is sequential, and the UID
cursor advances only after each new message is durably stored and marked read,
so restarts resume safely without consuming the existing mailbox backlog.

Watch memory use during the first inference. The 14B model is expected to use
roughly 10-11 GB on a 16 GB host. If the host is close to OOM, reduce
`NumCtx` from `8192` to `4096` in `internal/llm/llm.go` and rebuild, or add
swap. The configured body limit fits comfortably in a 4096-token context.

Useful commands:

```sh
docker compose ps
docker compose logs -f digestd ollama
docker compose restart digestd
make test
```

## Local Development

```sh
make test
make build
make dev-summarize
```

The dev summarizer reads `.fixtures/`, which is gitignored because it may
contain sensitive real mail.
