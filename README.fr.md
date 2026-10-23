<p align="center">
  <a href="https://flatrun.dev">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://flatrun.dev/flatrun-logo-white.svg">
      <img src="https://flatrun.dev/flatrun-logo.svg" alt="FlatRun" width="360">
    </picture>
  </a>
</p>

<p align="center">
  <a href="README.md">English</a> · Français · <a href="README.es.md">Español</a> · <a href="README.pt-BR.md">Português do Brasil</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

# Exécutez des applications conteneurisées sur vos propres serveurs

FlatRun permet de déployer, sécuriser, diagnostiquer, automatiser et gérer des
applications conteneurisées depuis une seule interface. Il exécute directement
des projets Docker Compose standards et peut faire évoluer les charges adaptées
avec Docker Swarm ou k3s.

Vos fichiers Compose, configurations et données restent sur vos machines. Vous
pouvez commencer avec un seul hôte Docker, puis connecter des serveurs et
activer l'orchestration lorsque la charge l'exige.

## Première application

Installez FlatRun sur Ubuntu ou Debian :

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/installer/main/scripts/install.sh | sudo bash
```

Ouvrez `http://<votre-serveur>:8080`, terminez la configuration, puis créez un
déploiement à partir d'un modèle, d'une image ou d'un fichier Compose. FlatRun
configure les conteneurs, le routage et HTTPS depuis le même tableau de bord.

Pour utiliser le terminal :

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/cli/main/scripts/install.sh | sudo sh
flatrun profile add production --url https://panel.example.com --token your-api-key-here
flatrun profile use production
flatrun health
```

## Aperçu

Chaque projet est un dossier avec un fichier Compose. Le tableau de bord les affiche sur un canevas, avec leur domaine, leur état et leurs services. Un projet ouvert montre ses services et leurs dépendances.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/projects-canvas-dark.png">
  <img alt="Les projets sur un canevas, chacun avec son logo, son domaine, son état et ses services" src="docs/screenshots/projects-canvas-light.png">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/project-architecture-dark.png">
  <img alt="Les services d'un projet sous forme de graphe" src="docs/screenshots/project-architecture-light.png">
</picture>


## Ce que FlatRun apporte

- Des déploiements conservés comme fichiers et projets Docker Compose standards.
- Des plans qui montrent les changements avant leur application.
- La gestion des certificats, sauvegardes, tâches planifiées et accès limités.
- Des métriques par conteneur exportables avec OpenTelemetry et Prometheus.
- La gestion de plusieurs serveurs depuis un seul tableau de bord.
- La mise à l'échelle avec Docker Swarm ou k3s pour les charges compatibles.
- Des notifications regroupées en incidents utiles.
- Des espaces de travail pour les équipes, avec propriétaires, administrateurs et membres.
- Des secrets chiffrés, renouvelables, jamais réaffichés, ou fournis par Infisical, Vault, OpenBao, Doppler ou 1Password.
- Des variables qui font référence à celles d'un autre projet, et un historique de chaque déploiement avec ses journaux.

Le tableau de bord, le CLI, GitHub Actions et les intégrations externes utilisent
la même API. La description OpenAPI exacte de l'agent installé est disponible à
l'adresse `/api/openapi.json`.

Consultez la [documentation complète](https://flatrun.dev/docs) ou le
[README anglais](README.md) pour la configuration, le développement et le
dépannage.

## Licence

Licence MIT. Consultez [LICENSE](LICENSE).
