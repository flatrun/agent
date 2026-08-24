# FlatRun

[English](README.md) | [Français](README.fr.md) | Español | [Português do Brasil](README.pt-BR.md) | [简体中文](README.zh-CN.md)

## Ejecuta aplicaciones en contenedores en tus propios servidores

FlatRun permite desplegar, proteger, diagnosticar, automatizar y administrar
aplicaciones en contenedores desde un solo lugar. Ejecuta proyectos Docker
Compose estándar directamente y puede escalar cargas compatibles mediante
Docker Swarm o k3s.

Tus archivos Compose, configuraciones y datos permanecen en tus máquinas.
Puedes comenzar con un solo host Docker, conectar más servidores y activar la
orquestación cuando la carga lo necesite.

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

El panel, el CLI, GitHub Actions y las integraciones externas utilizan la misma
API. La descripción OpenAPI exacta del agente instalado está disponible en
`/api/openapi.json`.

Consulta la [documentación completa](https://flatrun.dev/docs) o el
[README en inglés](README.md) para configuración, desarrollo y resolución de
problemas.

## Licencia

Licencia MIT. Consulta [LICENSE](LICENSE).
