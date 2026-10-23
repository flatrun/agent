<p align="center">
  <a href="https://flatrun.dev#gh-light-mode-only"><img src="docs/assets/flatrun-logo.svg" alt="FlatRun" width="360"></a>
  <a href="https://flatrun.dev#gh-dark-mode-only"><img src="docs/assets/flatrun-logo-white.svg" alt="FlatRun" width="360"></a>
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.fr.md">Français</a> · <a href="README.es.md">Español</a> · <a href="README.pt-BR.md">Português do Brasil</a> · 简体中文
</p>

**Railway 和 Render 的开源、自托管替代方案。** 在你自己的服务器上部署 Docker Compose 项目。

FlatRun 把 Railway 的工作方式带到你掌控的服务器上：由服务组成的项目、环境、可以回滚的部署、在项目之间共享的变量，以及常用应用的模板。每个项目都是一个包含标准 Compose 文件的目录，即使不再使用 FlatRun，也能继续用 `docker compose` 运行。

从一台服务器开始。在需要时连接更多服务器，并通过 Docker Swarm 或 k3s 扩展符合条件的服务。

![以图的形式展示一个项目的服务](docs/screenshots/project-architecture-light.png#gh-light-mode-only)
![以图的形式展示一个项目的服务](docs/screenshots/project-architecture-dark.png#gh-dark-mode-only)

## 部署第一个应用

在 Ubuntu 或 Debian 服务器上安装 FlatRun：

```bash
curl -fsSL https://get.flatrun.dev/install.sh | sudo bash
```

打开 `http://<你的服务器>:8080`，完成设置，然后通过模板、镜像或 Compose 文件创建
部署。FlatRun 在同一个控制面板中配置容器、路由和 HTTPS。

如需使用终端：

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/cli/main/scripts/install.sh | sudo sh
flatrun profile add production --url https://panel.example.com --token your-api-key-here
flatrun profile use production
flatrun health
```

## FlatRun 提供什么

- 部署保持为普通文件和标准 Docker Compose 项目。
- 在应用变更前查看计划。
- 管理证书、备份、计划任务和受限访问。
- 使用 OpenTelemetry 和 Prometheus 导出每个容器的指标。
- 从一个控制面板管理多台服务器。
- 使用 Docker Swarm 或 k3s 扩展符合条件的工作负载。
- 将相关通知合并为有用的事件。
- 面向团队的工作区，包含所有者、管理员和成员角色。
- 加密的密钥，可以轮换，保存后不再显示，也可以由 Infisical、Vault、OpenBao、Doppler 或 1Password 提供。
- 可以引用其他项目变量的变量，以及每次部署的记录和日志。

控制面板、CLI、GitHub Actions 和外部集成都使用同一个 API。已安装代理的准确
OpenAPI 描述位于 `/api/openapi.json`。

配置、开发和故障排除请参阅[完整文档](https://flatrun.dev/docs)或
[英文 README](README.md)。

## 许可证

MIT 许可证。请参阅 [LICENSE](LICENSE)。
