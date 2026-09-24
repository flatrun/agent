## Security

- Use strong, unique API keys.
- Generate a secure JWT secret: `openssl rand -base64 32`.
- Run behind a reverse proxy (nginx) with HTTPS in production.
- Restrict Docker socket access appropriately.
- Keep the agent updated.

### Troubleshooting

**Agent won't start:**
- Check Docker is running: `systemctl status docker`
- Verify Docker socket permissions: `ls -la /var/run/docker.sock`
- Validate the config YAML syntax

**Authentication issues:**
- Ensure the API key matches between UI and agent config
- Verify JWT secret is set
- Check CORS origins include your UI URL

**Can't see deployments:**
- Verify `deployments_path` exists and is readable
- Check each deployment has a valid `docker-compose.yml`
