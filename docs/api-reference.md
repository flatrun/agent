## API Reference

### Authentication

| Method | Endpoint | Description |
|---|---|---|
| POST | `/api/auth/login` | Login with API key |
| GET | `/api/auth/validate` | Validate JWT token |
| GET | `/api/auth/status` | Check auth status |

### Deployments

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/deployments` | List all deployments |
| POST | `/api/deployments` | Create new deployment |
| GET | `/api/deployments/:name` | Get deployment details |
| DELETE | `/api/deployments/:name` | Delete deployment |
| POST | `/api/deployments/:name/start` | Start deployment |
| POST | `/api/deployments/:name/stop` | Stop deployment |
| POST | `/api/deployments/:name/restart` | Restart deployment |
| GET | `/api/deployments/:name/logs` | Get deployment logs |

### Docker Resources

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/containers` | List containers |
| GET | `/api/images` | List images |
| GET | `/api/volumes` | List volumes |
| GET | `/api/networks` | List networks |
| POST | `/api/networks` | Create network |
| DELETE | `/api/networks/:name` | Delete network |

### Other

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/health` | Health check |
| GET | `/api/stats` | System statistics |
| GET | `/api/certificates` | List SSL certificates |
| GET | `/api/templates` | List Quick App templates |
| GET | `/api/plugins` | List installed plugins |
