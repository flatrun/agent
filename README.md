# FlatRun

[![CI](https://github.com/flatrun/agent/actions/workflows/ci.yml/badge.svg)](https://github.com/flatrun/agent/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

FlatRun turns one server into a self-operating platform: a hosting OS that deploys apps, terminates SSL, manages DNS, runs backups, and schedules jobs; a full platform API that automates all of it; and an agent engine that runs agents defined as plain markdown files, working under governance you set. It is a single Go binary with a web UI, a CLI, and GitHub Actions over standard Docker Compose. Everything it manages is plain files on disk, so you operate it with the tools you already know.

This repository is the **agent**: the Go service that runs deployments and serves the API the UI and CLI talk to.

## Why this exists

Most hosting panels trap you twice. Your data goes into named Docker volumes you can't easily inspect, and your setup goes into a database only the panel understands. The day you want to move hosts, debug a container, or stop paying for the UI, you find your infrastructure is only legible to the tool that created it.

FlatRun inverts that. Every app is a directory: a `docker-compose.yml` and its data and config sitting next to it. The panel reads and writes those files, but the files are the source of truth. Delete FlatRun tomorrow and every app keeps running under plain `docker compose`.

## What you get

- **Nothing hidden**: configs, data, certs, and env live in the filesystem, not in volumes you have to `docker inspect` to see.
- **Standard tools**: plain Docker Compose, operated with the `docker`, `docker compose`, and `git` you already know. Nothing proprietary to learn, and FlatRun stays optional.
- **Copy = migrate**: a deployment is a directory. `tar` it, move it, `docker compose up -d`. Backups are file backups.
- **Built-in observability**: per-container metrics are collected as time series using OpenTelemetry container semantic conventions, exported over OTLP to any backend you point them at, and served for scraping in Prometheus format, so Grafana, SigLens, SigNoz or anything else reads exactly what the built-in UI draws. No separate collector to run, and no lock-in on your own numbers.
- **S3-compatible backups**: schedule deployment backups to any S3-compatible object store. Set your own endpoint and bucket; it is not tied to AWS.
- **AI-native operation**: a built-in assistant reads a deployment's logs and config and answers in plain operator terms: diagnose a failure, suggest improvements, harden security, or explain what a stack is doing. It carries out changes under governance you set. It is off until you configure a provider, and secrets are redacted before anything is sent.
- **Agent-native**: define agents as plain markdown files, or describe one to the assistant and let it write the file. The runtime executes them through governed tools: policies decide what runs freely, what needs your sign-off, and what never runs. The same tool set is exposed over a standard MCP server for external agents. See [docs/AGENTS.md](docs/AGENTS.md).
- **Automatable end to end**: the same actions are available from the UI, the `flatrun` CLI, and a GitHub Action, so a deploy is a dashboard click or a CI step.

## Quick start

Install FlatRun on any Ubuntu/Debian server:

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/installer/main/scripts/install.sh | sudo bash
```

This installs three pieces:

- **Agent** (`:8090`): this service, running as a systemd unit
- **UI** (`:8080`): the dashboard
- **Nginx** (`:80`, `:443`): reverse proxy for your deployments, running as a container

Open the dashboard at `http://<your-server>:8080` and deploy your first app.

## Components

FlatRun is a set of focused pieces, each in its own repository:

| Piece | What it does |
|---|---|
| [Agent](https://github.com/flatrun/agent) | This repo. Go service that manages Docker Compose deployments and serves the REST API. Home of the AI assistant and app templates. |
| [UI](https://github.com/flatrun/ui) | Vue 3 dashboard: deployments, live logs, resource and SSL monitoring. |
| [CLI](https://github.com/flatrun/cli) | `flatrun`, the operator and automation surface over the agent API. |
| [Actions](https://github.com/flatrun/actions) | GitHub Actions to install the CLI and deploy from a workflow. |
| [Installer](https://github.com/flatrun/installer) | One-command server install and image builds. |

## Build from source

For local development or a manual install.

### Prerequisites

- Go 1.21+
- Docker & Docker Compose v2
- Access to the Docker socket (`/var/run/docker.sock`)
- Linux server (Ubuntu 20.04+, Debian 11+, or similar)

### Build

```bash
git clone https://github.com/flatrun/agent.git
cd agent

make build
# or manually:
go mod download
go build -o flatrun-agent ./cmd/agent
```

A prebuilt binary is published with each release:

```bash
wget https://github.com/flatrun/agent/releases/latest/download/flatrun-agent
chmod +x flatrun-agent
```

## Configuration

Copy the example config and edit it:

```bash
cp config.example.yml config.yml
```

```yaml
deployments_path: /home/user/deployments
docker_socket: unix:///var/run/docker.sock

api:
  host: 0.0.0.0
  port: 8090
  enable_cors: true
  allowed_origins:
    - http://localhost:5173
    - http://localhost:3000

auth:
  enabled: true
  api_keys:
    - "your-secure-api-key-here"
  jwt_secret: "generate-a-secure-random-string-here"

nginx:
  container_name: nginx
  config_path: /deployments/nginx/conf.d
  reload_command: nginx -s reload

certbot:
  container_name: certbot
  email: your-email@example.com
  staging: true

logging:
  level: info
  format: json

health:
  check_interval: 30s
  metrics_retention: 24h
```

Key options:

- **deployments_path**: directory where your docker-compose deployments live
- **api.port**: port the API server listens on (default: 8090)
- **auth.api_keys**: valid API keys for authentication
- **auth.jwt_secret**: secret for signing JWT tokens; generate with `openssl rand -base64 32`

## Running the agent

### Development

```bash
make run
# or directly
./flatrun-agent --config config.yml
```

### Production with systemd

```bash
sudo mv flatrun-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/flatrun-agent
sudo nano /etc/systemd/system/flatrun-agent.service
```

```ini
[Unit]
Description=FlatRun Agent
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/flatrun
ExecStart=/usr/local/bin/flatrun-agent --config /opt/flatrun/config.yml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable flatrun-agent
sudo systemctl start flatrun-agent

sudo systemctl status flatrun-agent
sudo journalctl -u flatrun-agent -f
```

## Documentation

Full documentation, guides, and the API reference live at [flatrun.dev/docs](https://flatrun.dev/docs).

## Security

- Use strong, unique API keys.
- Generate a secure JWT secret (`openssl rand -base64 32`).
- Run behind a reverse proxy with HTTPS in production.
- Restrict Docker socket access appropriately.
- The AI assistant is off until you configure a provider, and secrets are redacted before context is sent.

## Troubleshooting

**Agent won't start**
- Check Docker is running: `systemctl status docker`
- Verify Docker socket permissions: `ls -la /var/run/docker.sock`
- Validate your `config.yml` YAML syntax

**Authentication issues**
- Ensure the API key matches between the UI and the agent config
- Verify the JWT secret is set
- Check CORS origins include your UI URL

**Can't see deployments**
- Verify `deployments_path` exists and is readable
- Check each deployment has a valid `docker-compose.yml`

## Contributing

FlatRun is open source and moving fast. If the flat-file approach resonates, the fastest ways to help:

- **Star the repo** so more people building on Docker Compose find it.
- **Open an issue** for a bug, a missing template, or a hosting workflow that feels harder than it should.
- **Send a PR** for a fix, a new app template, or docs. Start with a good first issue if you want a scoped entry point.

## License

MIT License. See [LICENSE](LICENSE).
