## Overview

FlatRun Agent is the backend service that manages Docker deployments through a simple filesystem-based approach. It monitors your deployments directory and provides a REST API for the FlatRun UI.

### Features

- Docker Compose deployment management
- Container lifecycle control (start, stop, restart)
- Real-time container monitoring and logs
- Docker resource management (images, volumes, networks)
- SSL certificate monitoring
- Quick App templates for rapid deployment
- JWT-based API authentication
- Health monitoring and statistics
- Plugin architecture for extensibility

### Architecture

The agent follows a flat-file approach: each deployment is a directory containing a `docker-compose.yml` file. The agent watches the configured deployments directory and manages the containers through the Docker API. It communicates with the FlatRun UI through a REST API and uses JWT tokens for authentication.
