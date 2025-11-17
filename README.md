# FlatRun Agent

Lightweight Go agent for flat-file container orchestration.

## Overview

FlatRun Agent is the backend service that manages Docker deployments through a simple filesystem-based approach. It monitors your deployments directory and provides a REST API for the FlatRun UI.

## Features

- Docker Compose deployment management
- Container lifecycle control (start, stop, restart)
- Real-time container monitoring and logs
- Docker resource management (images, volumes, networks)
- SSL certificate monitoring
- Quick App templates for rapid deployment
- JWT-based API authentication
- Health monitoring and statistics

## Installation

### Prerequisites

- Go 1.21+ (for building from source)
- Docker & Docker Compose v2
- Access to Docker socket (`/var/run/docker.sock`)
- Linux server (Ubuntu 20.04+, Debian 11+, or similar)

### Option 1: Build from Source

```bash
# Clone the repository
git clone https://github.com/flatrun/agent.git
cd agent

# Install dependencies and build
make build

# Or manually:
go mod download
go build -o flatrun-agent ./cmd/agent
```

### Option 2: Download Binary

```bash
# Download the latest release (when available)
wget https://github.com/flatrun/agent/releases/latest/download/flatrun-agent
chmod +x flatrun-agent
```

## Configuration

1. Copy the example configuration:

```bash
cp config.example.yml config.yml
```

2. Edit `config.yml`:

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

### Key Configuration Options

- **deployments_path**: Directory where your docker-compose deployments are stored
- **api.port**: Port the API server listens on (default: 8090)
- **auth.api_keys**: List of valid API keys for authentication
- **auth.jwt_secret**: Secret key for signing JWT tokens (generate a secure random string)

## Running the Agent

### Development Mode

```bash
# Using make
make run

# Or directly
./flatrun-agent --config config.yml
```

### Production Setup with Systemd

1. Move the binary to a system location:

```bash
sudo mv flatrun-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/flatrun-agent
```

2. Create a systemd service file:

```bash
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

3. Enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable flatrun-agent
sudo systemctl start flatrun-agent

# Check status
sudo systemctl status flatrun-agent

# View logs
sudo journalctl -u flatrun-agent -f
```

## Directory Structure

```
/etc/flatrun/                    # FlatRun configuration (example location)
└── config.yml                   # Agent configuration

/usr/local/bin/
└── flatrun-agent                # Agent binary

/path/to/deployments/            # Configurable via deployments_path
├── .flatrun/                    # FlatRun metadata
│   └── templates/               # Quick App templates
├── myapp/                       # Example deployment
│   └── docker-compose.yml
└── nginx/                       # Nginx reverse proxy
    └── conf.d/
```

The deployments directory can be located anywhere on your system (e.g., `/home/user/deployments`, `/srv/deployments`, `/var/lib/flatrun/deployments`). Configure it via `deployments_path` in your config file.

## API Endpoints

### Authentication
- `POST /api/auth/login` - Login with API key
- `GET /api/auth/validate` - Validate JWT token
- `GET /api/auth/status` - Check auth status

### Deployments
- `GET /api/deployments` - List all deployments
- `POST /api/deployments` - Create new deployment
- `GET /api/deployments/:name` - Get deployment details
- `DELETE /api/deployments/:name` - Delete deployment
- `POST /api/deployments/:name/start` - Start deployment
- `POST /api/deployments/:name/stop` - Stop deployment
- `POST /api/deployments/:name/restart` - Restart deployment
- `GET /api/deployments/:name/logs` - Get deployment logs

### Docker Resources
- `GET /api/containers` - List containers
- `GET /api/images` - List images
- `GET /api/volumes` - List volumes
- `GET /api/networks` - List networks
- `POST /api/networks` - Create network
- `DELETE /api/networks/:name` - Delete network

### Other
- `GET /api/health` - Health check
- `GET /api/stats` - System statistics
- `GET /api/certificates` - List SSL certificates
- `GET /api/templates` - List Quick App templates
- `GET /api/plugins` - List installed plugins

## Security Considerations

- Always use strong, unique API keys
- Generate a secure JWT secret (use `openssl rand -base64 32`)
- Run behind a reverse proxy (nginx) with HTTPS in production
- Restrict Docker socket access appropriately
- Keep the agent updated

## Development

```bash
# Run tests
make test

# Clean build artifacts
make clean

# Run in development mode with hot reload (requires air)
air
```

## Troubleshooting

### Agent won't start
- Check Docker is running: `systemctl status docker`
- Verify Docker socket permissions: `ls -la /var/run/docker.sock`
- Check config file syntax: validate your YAML

### Authentication issues
- Ensure API key matches between UI and agent config
- Verify JWT secret is set
- Check CORS origins include your UI URL

### Can't see deployments
- Verify `deployments_path` exists and is readable
- Check each deployment has a valid `docker-compose.yml`

## License

MIT License - See [LICENSE](LICENSE) file
