# Flatrun Plugin Architecture

## Overview

Flatrun supports a plugin architecture that allows extending the platform with custom functionality. Plugins can add new deployment types, dashboard widgets, API endpoints, and automated workflows.

## Core Concepts

### Agent as Source of Truth
The Go agent serves as the single source of truth. All plugins register with the agent, which:
- Maintains plugin registry
- Exposes plugin metadata to the UI
- Manages plugin lifecycle
- Provides plugin APIs

### Plugin Types

1. **Deployment Plugins** - Custom app deployment templates (WordPress, Laravel, etc.)
2. **Widget Plugins** - Dashboard components showing custom metrics/controls
3. **Service Plugins** - Background services (monitoring, backups, etc.)
4. **Integration Plugins** - External service integrations (DNS providers, storage, etc.)

## Plugin Structure

```
plugins/
  wordpress/
    plugin.yaml           # Plugin manifest
    templates/
      docker-compose.yml  # Deployment template
      nginx.conf          # Nginx configuration
    hooks/
      pre_install.sh      # Pre-installation hook
      post_install.sh     # Post-installation hook
    ui/
      widget.json         # Widget configuration
      icon.svg            # Plugin icon
```

## Plugin Manifest (plugin.yaml)

```yaml
name: wordpress
version: 1.0.0
display_name: WordPress
description: Managed WordPress hosting with automatic updates
author: Flatrun
license: proprietary

# Plugin capabilities
type: deployment
category: cms

# Dependencies
requires:
  - mysql
  - nginx

# Configuration schema
config_schema:
  type: object
  properties:
    site_title:
      type: string
      title: Site Title
      required: true
    admin_email:
      type: string
      format: email
      title: Admin Email
      required: true
    php_version:
      type: string
      enum: ["8.1", "8.2", "8.3"]
      default: "8.2"
      title: PHP Version
    enable_cache:
      type: boolean
      default: true
      title: Enable Redis Cache
    auto_updates:
      type: boolean
      default: true
      title: Automatic Updates

# Dashboard widget
widget:
  enabled: true
  position: main
  size: medium
  refresh_interval: 60
  actions:
    - name: clear_cache
      label: Clear Cache
      icon: pi-trash
    - name: update_core
      label: Update WordPress
      icon: pi-refresh

# API endpoints provided by plugin
api:
  - path: /plugins/wordpress/:id/cache
    method: DELETE
    handler: clear_cache
  - path: /plugins/wordpress/:id/update
    method: POST
    handler: update_wordpress

# Hooks
hooks:
  pre_install: hooks/pre_install.sh
  post_install: hooks/post_install.sh
  pre_uninstall: hooks/pre_uninstall.sh
  health_check: hooks/health_check.sh

# Resource requirements
resources:
  min_memory: 512M
  min_cpu: 0.5
  recommended_memory: 2G
  recommended_cpu: 2

# Networks
networks:
  - web
  - database
```

## Agent Plugin System

### Plugin Interface (Go)

```go
package plugins

type Plugin interface {
    // Metadata
    Name() string
    Version() string
    Type() PluginType

    // Lifecycle
    Initialize(config map[string]interface{}) error
    Start() error
    Stop() error

    // Capabilities
    GetCapabilities() []Capability

    // API
    RegisterRoutes(router *gin.RouterGroup) error

    // Widget
    GetWidgetData() (interface{}, error)
}

type DeploymentPlugin interface {
    Plugin

    // Deployment operations
    CreateDeployment(name string, config map[string]interface{}) error
    ConfigureDeployment(name string, config map[string]interface{}) error
    GetDeploymentStatus(name string) (*DeploymentStatus, error)

    // Templates
    GetDockerCompose(config map[string]interface{}) (string, error)
    GetNginxConfig(config map[string]interface{}) (string, error)
}

type PluginType string

const (
    TypeDeployment  PluginType = "deployment"
    TypeWidget      PluginType = "widget"
    TypeService     PluginType = "service"
    TypeIntegration PluginType = "integration"
)

type Capability string

const (
    CapAutoSSL      Capability = "auto_ssl"
    CapAutoBackup   Capability = "auto_backup"
    CapAutoUpdate   Capability = "auto_update"
    CapMonitoring   Capability = "monitoring"
    CapScaling      Capability = "scaling"
)
```

### Plugin Registry

```go
package plugins

type Registry struct {
    plugins map[string]Plugin
    mu      sync.RWMutex
}

func (r *Registry) Register(plugin Plugin) error
func (r *Registry) Unregister(name string) error
func (r *Registry) Get(name string) (Plugin, bool)
func (r *Registry) List() []PluginInfo
func (r *Registry) LoadFromDisk(path string) error
```

## UI Plugin Integration

### Plugin Registry API

```
GET /api/plugins
Response:
{
  "plugins": [
    {
      "name": "wordpress",
      "display_name": "WordPress",
      "version": "1.0.0",
      "type": "deployment",
      "enabled": true,
      "widget": {
        "enabled": true,
        "position": "main",
        "size": "medium"
      },
      "config_schema": { ... }
    }
  ]
}
```

### Widget Injection

The UI fetches widget configurations from the agent and dynamically renders them:

```vue
<!-- DynamicWidget.vue -->
<template>
  <component
    :is="widgetComponent"
    :plugin="plugin"
    :data="widgetData"
    @action="handleAction"
  />
</template>
```

### Shared Component Library

Plugins use pre-built components to maintain UI consistency:

- `FlatCard` - Standard card container
- `FlatButton` - Styled buttons
- `FlatInput` - Form inputs
- `FlatTable` - Data tables
- `FlatModal` - Modal dialogs
- `FlatStatus` - Status indicators
- `FlatMetric` - Metric displays
- `FlatChart` - Charts and graphs

## Example: WordPress Plugin

### Deployment Template

```yaml
# templates/docker-compose.yml
services:
  wordpress:
    image: wordpress:php${PHP_VERSION}-apache
    container_name: ${NAME}_wordpress
    environment:
      WORDPRESS_DB_HOST: ${NAME}_db
      WORDPRESS_DB_NAME: wordpress
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ./wordpress:/var/www/html
    networks:
      - web
      - ${NAME}_internal
    depends_on:
      - db
    restart: unless-stopped

  db:
    image: mysql:8.0
    container_name: ${NAME}_db
    environment:
      MYSQL_DATABASE: wordpress
      MYSQL_USER: wordpress
      MYSQL_PASSWORD: ${DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./mysql:/var/lib/mysql
    networks:
      - ${NAME}_internal
    restart: unless-stopped

  redis:
    image: redis:alpine
    container_name: ${NAME}_redis
    volumes:
      - ./redis:/data
    networks:
      - ${NAME}_internal
    restart: unless-stopped

networks:
  web:
    external: true
  ${NAME}_internal:
    driver: bridge
```

### Widget Data

```json
{
  "type": "wordpress",
  "metrics": {
    "posts": 125,
    "pages": 12,
    "users": 5,
    "plugins": 18,
    "themes": 3,
    "updates_available": 4
  },
  "health": {
    "status": "healthy",
    "php_version": "8.2.10",
    "wp_version": "6.4.2",
    "disk_usage": "2.1GB",
    "database_size": "156MB"
  },
  "actions": [
    {
      "id": "clear_cache",
      "label": "Clear Cache",
      "enabled": true
    },
    {
      "id": "update_core",
      "label": "Update WordPress",
      "enabled": true,
      "badge": "4 updates"
    }
  ]
}
```

## Example: Laravel Plugin

### Configuration Schema

```yaml
config_schema:
  type: object
  properties:
    app_name:
      type: string
      title: Application Name
    environment:
      type: string
      enum: ["local", "staging", "production"]
      default: "production"
    php_version:
      type: string
      enum: ["8.1", "8.2", "8.3"]
      default: "8.2"
    queue_driver:
      type: string
      enum: ["sync", "redis", "database"]
      default: "redis"
    cache_driver:
      type: string
      enum: ["file", "redis", "memcached"]
      default: "redis"
    enable_horizon:
      type: boolean
      default: true
    enable_scheduler:
      type: boolean
      default: true
```

## Security Considerations

1. **Sandboxing**: Plugins run in isolated environments
2. **Permissions**: Fine-grained permission system for plugin actions
3. **Validation**: All plugin configs validated against schema
4. **Signing**: Plugins can be cryptographically signed
5. **Audit**: All plugin actions are logged

## Installation Flow

1. User selects plugin from marketplace or uploads
2. Agent validates plugin manifest
3. Dependencies checked and resolved
4. Plugin files copied to plugins directory
5. Agent loads plugin and registers capabilities
6. UI receives updated plugin registry
7. Dashboard displays new widgets/options

## Future Enhancements

- Plugin marketplace (hosted/self-hosted)
- Hot-reload without agent restart
- Plugin versioning and migrations
- Plugin dependencies and conflicts resolution
- Multi-tenant plugin permissions
- Plugin analytics and telemetry
