## Installation

### Prerequisites

- Go 1.21 or later (for building from source)
- Docker and Docker Compose v2
- Access to Docker socket (`/var/run/docker.sock`)
- Linux server (Ubuntu 20.04+, Debian 11+, or similar)

### Option 1: Build from Source

```bash
git clone https://github.com/flatrun/agent.git
cd agent
make build
```

Or manually:

```bash
go mod download
go build -o flatrun-agent ./cmd/agent
```

### Option 2: Download Binary

```bash
wget https://github.com/flatrun/agent/releases/latest/download/flatrun-agent
chmod +x flatrun-agent
```

### Running

Development mode:

```bash
make run
# or
./flatrun-agent --config config.yml
```

### Production with Systemd

Move the binary to a system location:

```bash
sudo mv flatrun-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/flatrun-agent
```

Prepare the working directory and a dedicated user:

```bash
sudo mkdir -p /opt/flatrun
sudo useradd -r -s /usr/sbin/nologin -G docker flatrun
sudo chown flatrun:flatrun /opt/flatrun
sudo cp config.yml /opt/flatrun/config.yml
```

Create `/etc/systemd/system/flatrun-agent.service`:

```ini
[Unit]
Description=FlatRun Agent
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=flatrun
Group=docker
WorkingDirectory=/opt/flatrun
ExecStart=/usr/local/bin/flatrun-agent --config /opt/flatrun/config.yml
# Make sure config.yml is copied to /opt/flatrun/ first
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable flatrun-agent
sudo systemctl start flatrun-agent
sudo systemctl status flatrun-agent
sudo journalctl -u flatrun-agent -f
```
