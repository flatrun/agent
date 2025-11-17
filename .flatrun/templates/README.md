# Deployment Templates

Place your deployment templates here. Each template should be in its own directory with:

- `metadata.yml` - Template metadata (name, description, icon, category)
- `docker-compose.yml` - The actual compose configuration

## Metadata Format

```yaml
name: My Template
description: Short description of the template
icon: pi pi-box  # PrimeIcons class name
category: framework  # cms, framework, database, etc.
```

## Compose Template Variables

- `${NAME}` - Will be replaced with the deployment name

## Example Structure

```
templates/
  wordpress/
    metadata.yml
    docker-compose.yml
  laravel/
    metadata.yml
    docker-compose.yml
```
