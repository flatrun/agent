<p align="center">
  <a href="https://flatrun.dev">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://flatrun.dev/flatrun-logo-white.svg">
      <img src="https://flatrun.dev/flatrun-logo.svg" alt="FlatRun" width="360">
    </picture>
  </a>
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.fr.md">Français</a> · <a href="README.es.md">Español</a> · Português do Brasil · <a href="README.zh-CN.md">简体中文</a>
</p>

**Alternativa de código aberto e auto-hospedada ao Railway e ao Render.** Implante projetos Docker Compose nos seus próprios servidores.

O FlatRun leva o fluxo de trabalho do Railway para um servidor que você controla: projetos formados por serviços, ambientes, implantações que você pode reverter, variáveis compartilhadas entre projetos e modelos para aplicações comuns. Cada projeto é uma pasta com um arquivo Compose padrão, então continua rodando com `docker compose` se você parar de usar o FlatRun.

Comece com um servidor. Conecte outros e escale os serviços compatíveis com Docker Swarm ou k3s quando a carga precisar.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/project-architecture-dark.png">
  <img alt="Os serviços de um projeto como um grafo" src="docs/screenshots/project-architecture-light.png">
</picture>

## Primeira aplicação

Instale o FlatRun no Ubuntu ou Debian:

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/installer/main/scripts/install.sh | sudo bash
```

Abra `http://<seu-servidor>:8080`, conclua a configuração e crie uma implantação
a partir de um modelo, uma imagem ou um arquivo Compose. O FlatRun configura os
contêineres, o roteamento e o HTTPS no mesmo painel.

Para trabalhar pelo terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/cli/main/scripts/install.sh | sudo sh
flatrun profile add production --url https://panel.example.com --token your-api-key-here
flatrun profile use production
flatrun health
```

## O que o FlatRun oferece

- Implantações mantidas como arquivos e projetos Docker Compose padrão.
- Planos que mostram as mudanças antes da aplicação.
- Gestão de certificados, backups, tarefas agendadas e acesso limitado.
- Métricas por contêiner exportáveis com OpenTelemetry e Prometheus.
- Gestão de vários servidores em um único painel.
- Escalabilidade com Docker Swarm ou k3s para cargas compatíveis.
- Notificações relacionadas agrupadas em incidentes úteis.
- Espaços de trabalho para equipes, com proprietários, administradores e membros.
- Segredos criptografados, rotacionáveis e nunca exibidos novamente, ou fornecidos por Infisical, Vault, OpenBao, Doppler ou 1Password.
- Variáveis que referenciam as de outro projeto, e um histórico de cada implantação com seus logs.

O painel, o CLI, o GitHub Actions e as integrações externas usam a mesma API. A
descrição OpenAPI exata do agente instalado está disponível em
`/api/openapi.json`.

Consulte a [documentação completa](https://flatrun.dev/docs) ou o
[README em inglês](README.md) para configuração, desenvolvimento e solução de
problemas.

## Licença

Licença MIT. Consulte [LICENSE](LICENSE).
