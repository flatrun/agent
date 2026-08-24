# FlatRun

[English](README.md) | Français | [Español](README.es.md) | [Português do Brasil](README.pt-BR.md) | [简体中文](README.zh-CN.md)

## Exécutez des applications conteneurisées sur vos propres serveurs

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

## Ce que FlatRun apporte

- Des déploiements conservés comme fichiers et projets Docker Compose standards.
- Des plans qui montrent les changements avant leur application.
- La gestion des certificats, sauvegardes, tâches planifiées et accès limités.
- Des métriques par conteneur exportables avec OpenTelemetry et Prometheus.
- La gestion de plusieurs serveurs depuis un seul tableau de bord.
- La mise à l'échelle avec Docker Swarm ou k3s pour les charges compatibles.
- Des notifications regroupées en incidents utiles.

Le tableau de bord, le CLI, GitHub Actions et les intégrations externes utilisent
la même API. La description OpenAPI exacte de l'agent installé est disponible à
l'adresse `/api/openapi.json`.

Consultez la [documentation complète](https://flatrun.dev/docs) ou le
[README anglais](README.md) pour la configuration, le développement et le
dépannage.

## Licence

Licence MIT. Consultez [LICENSE](LICENSE).
