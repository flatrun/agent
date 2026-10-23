<p align="center">
  <a href="https://flatrun.dev">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://flatrun.dev/flatrun-logo-white.svg">
      <img src="https://flatrun.dev/flatrun-logo.svg" alt="FlatRun" width="360">
    </picture>
  </a>
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.fr.md">Français</a> · <a href="README.es.md">Español</a> · <a href="README.pt-BR.md">Português do Brasil</a> · 简体中文
</p>

# 在自己的服务器上运行容器应用

FlatRun 让你从一个位置部署、保护、诊断、自动化和管理容器应用。它直接运行标准
Docker Compose 项目，也可以通过 Docker Swarm 或 k3s 扩展符合条件的工作负载。

Compose 文件、配置和数据始终保留在你的机器上。你可以从一台 Docker 主机开始，
在需要时连接更多服务器并启用编排。

## 部署第一个应用

在 Ubuntu 或 Debian 服务器上安装 FlatRun：

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/installer/main/scripts/install.sh | sudo bash
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

## 预览

每个项目都是一个包含 Compose 文件的目录。控制面板把项目画在画布上，显示域名、状态和服务。打开一个项目可以看到它的服务以及服务之间的依赖关系。

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/projects-canvas-dark.png">
  <img alt="画布上的项目，每个卡片显示标志、域名、状态和服务" src="docs/screenshots/projects-canvas-light.png">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/project-architecture-dark.png">
  <img alt="以图的形式展示一个项目的服务" src="docs/screenshots/project-architecture-light.png">
</picture>


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
