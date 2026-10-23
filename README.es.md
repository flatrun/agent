<p align="center">
  <a href="https://flatrun.dev#gh-light-mode-only"><img src="docs/assets/flatrun-logo.svg" alt="FlatRun" width="360"></a>
  <a href="https://flatrun.dev#gh-dark-mode-only"><img src="docs/assets/flatrun-logo-white.svg" alt="FlatRun" width="360"></a>
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.fr.md">Français</a> · Español · <a href="README.pt-BR.md">Português do Brasil</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

**Alternativa de código abierto y autoalojada a Railway y Render.** Despliega proyectos Docker Compose en tus propios servidores.

FlatRun lleva el flujo de trabajo de Railway a un servidor que tú controlas: proyectos formados por servicios, entornos, despliegues que puedes revertir, variables compartidas entre proyectos y plantillas para aplicaciones comunes. Cada proyecto es una carpeta con un archivo Compose estándar, así que sigue funcionando con `docker compose` si dejas de usar FlatRun.

Empieza con un servidor. Conecta más y escala los servicios compatibles con Docker Swarm o k3s cuando la carga lo necesite.

![Los servicios de un proyecto como un grafo](docs/screenshots/project-architecture-light.png#gh-light-mode-only)
![Los servicios de un proyecto como un grafo](docs/screenshots/project-architecture-dark.png#gh-dark-mode-only)

## Primera aplicación

Instala FlatRun en Ubuntu o Debian:

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/installer/main/scripts/install.sh | sudo bash
```

Abre `http://<tu-servidor>:8080`, completa la configuración y crea un despliegue
desde una plantilla, una imagen o un archivo Compose. FlatRun configura los
contenedores, el enrutamiento y HTTPS desde el mismo panel.

Para trabajar desde el terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/flatrun/cli/main/scripts/install.sh | sudo sh
flatrun profile add production --url https://panel.example.com --token your-api-key-here
flatrun profile use production
flatrun health
```

## Qué aporta FlatRun

- Despliegues guardados como archivos y proyectos Docker Compose estándar.
- Planes que muestran los cambios antes de aplicarlos.
- Gestión de certificados, copias de seguridad, tareas programadas y acceso limitado.
- Métricas por contenedor exportables mediante OpenTelemetry y Prometheus.
- Gestión de varios servidores desde un solo panel.
- Escalado mediante Docker Swarm o k3s para cargas compatibles.
- Notificaciones relacionadas agrupadas en incidentes útiles.
- Espacios de trabajo para equipos, con propietarios, administradores y miembros.
- Secretos cifrados, rotables y que nunca se vuelven a mostrar, o aportados por Infisical, Vault, OpenBao, Doppler o 1Password.
- Variables que hacen referencia a las de otro proyecto, y un historial de cada despliegue con sus registros.

El panel, el CLI, GitHub Actions y las integraciones externas utilizan la misma
API. La descripción OpenAPI exacta del agente instalado está disponible en
`/api/openapi.json`.

Consulta la [documentación completa](https://flatrun.dev/docs) o el
[README en inglés](README.md) para configuración, desarrollo y resolución de
problemas.

## Licencia

Licencia MIT. Consulta [LICENSE](LICENSE).
