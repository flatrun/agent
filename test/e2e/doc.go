/*
Package e2e contains end-to-end tests for the FlatRun agent.

# Prerequisites

Before running E2E tests, ensure:
1. FlatRun agent is running
2. Docker is available
3. Required environment variables are set

# Environment Variables

	FLATRUN_API_URL          - Agent API URL (default: http://localhost:8080/api)
	FLATRUN_AUTH_TOKEN       - API authentication token (if auth enabled)
	FLATRUN_DEPLOYMENTS_PATH - Path where deployments are stored (default: /tmp/flatrun-test-deployments)

# Running Tests

Run all E2E tests:

	go test -v ./test/e2e/...

Run specific test:

	go test -v ./test/e2e/... -run TestStaticDeployment

Run without long-running tests:

	go test -v -short ./test/e2e/...

# Test Categories

## Unit Tests (templates/templates_test.go)
Tests for the embedded templates package:
- Template listing
- Metadata parsing
- Compose content validation
- Priority values

## Template API Tests (test/e2e/templates_test.go)
Tests for the /templates API endpoints:
- GET /templates returns sorted templates
- POST /templates/refresh updates templates
- Template content validation

## Static Deployment Tests (test/e2e/static_test.go)
Tests for static site deployments:
- HTML file creation with template hooks
- ${NAME} placeholder replacement
- Compose file structure
- Domain serving (requires proxy)

## WordPress Deployment Tests (test/e2e/wordpress_test.go)
Tests for WordPress deployments:
- Compose file creation
- Database environment variables
- Shared database integration
- Domain serving (requires proxy + database)

# Notes

- Some tests require the nginx proxy to be configured for *.localhost domains
- WordPress tests may require a running MySQL/MariaDB container
- Tests clean up after themselves but may leave artifacts on failure
*/
package e2e
