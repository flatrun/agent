# FlatRun

[English](README.md) | [Français](README.fr.md) | [Español](README.es.md) | [Português do Brasil](README.pt-BR.md) | 简体中文

## 在自己的服务器上运行容器应用

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

## FlatRun 提供什么

- 部署保持为普通文件和标准 Docker Compose 项目。
- 在应用变更前查看计划。
- 管理证书、备份、计划任务和受限访问。
- 使用 OpenTelemetry 和 Prometheus 导出每个容器的指标。
- 从一个控制面板管理多台服务器。
- 使用 Docker Swarm 或 k3s 扩展符合条件的工作负载。
- 将相关通知合并为有用的事件。

控制面板、CLI、GitHub Actions 和外部集成都使用同一个 API。已安装代理的准确
OpenAPI 描述位于 `/api/openapi.json`。

配置、开发和故障排除请参阅[完整文档](https://flatrun.dev/docs)或
[英文 README](README.md)。

## 许可证

MIT 许可证。请参阅 [LICENSE](LICENSE)。
