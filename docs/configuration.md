## Configuration

Copy the example configuration and edit it:

```bash
cp config.example.yml config.yml
```

### Reference

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
  jwt_secret: "REPLACE_WITH_SECURE_RANDOM_STRING"  # generate via: openssl rand -base64 32

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

### Key Options

| Option | Description |
|---|---|
| `deployments_path` | Directory containing docker-compose deployments |
| `api.port` | API server port (default: 8090) |
| `auth.api_keys` | Valid API keys for authentication |
| `auth.jwt_secret` | Secret for signing JWT tokens |
| `logging.level` | Log level: debug, info, warn, error |
| `health.check_interval` | Health check frequency |
