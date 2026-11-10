<p align="center">
  <a href="https://flatrun.dev#gh-light-mode-only"><img src="docs/assets/flatrun-logo.svg" alt="FlatRun" width="360"></a>
  <a href="https://flatrun.dev#gh-dark-mode-only"><img src="docs/assets/flatrun-logo-white.svg" alt="FlatRun" width="360"></a>
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.fr.md">Français</a> · <a href="README.es.md">Español</a> · Português do Brasil · <a href="README.zh-CN.md">简体中文</a>
</p>

**Alternativa de código aberto ao Railway, ao Render e ao E2B.** Implante aplicações, execute sandboxes para agentes de IA e rode jobs de CI a partir do GitHub, do GitLab ou de qualquer webhook nos seus próprios servidores.

O FlatRun leva o fluxo de trabalho do Railway para um servidor que você controla: projetos formados por serviços, ambientes, implantações que você pode reverter, variáveis compartilhadas entre projetos e modelos para aplicações comuns. Cada projeto é uma pasta com um arquivo Compose padrão, então continua rodando com `docker compose` se você parar de usar o FlatRun.

Comece com um servidor. Conecte outros e escale os serviços compatíveis com Docker Swarm ou k3s quando a carga precisar.

![Os serviços de um projeto como um grafo](docs/screenshots/project-architecture-light.png#gh-light-mode-only)
![Os serviços de um projeto como um grafo](docs/screenshots/project-architecture-dark.png#gh-dark-mode-only)

## Primeira aplicação

Instale o FlatRun no Ubuntu ou Debian:

```bash
curl -fsSL https://get.flatrun.dev/install.sh | sudo bash
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
- Variáveis que referenciam as de outro projeto, e o histórico das últimas 50 implantações com seus logs.
- Sandboxes que executam um comando em uma microVM Firecracker nova, ou em um contêiner Docker para código confiável, sem rede por padrão, e coletam a saída e os arquivos pedidos.
- Jobs de CI: cada job do GitHub Actions com seus rótulos recebe um runner auto-hospedado de uso único, e um push ou webhook do GitHub, do GitLab ou de qualquer serviço pode iniciar um comando em uma instância nova.

O painel, o CLI, o GitHub Actions e as integrações externas usam a mesma API. A
descrição OpenAPI exata do agente instalado está disponível em
`/api/openapi.json`.

Consulte a [documentação completa](https://flatrun.dev/docs) ou o
[README em inglês](README.md) para configuração, desenvolvimento e solução de
problemas.

## Licença

Licença MIT. Consulte [LICENSE](LICENSE).
