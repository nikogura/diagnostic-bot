.PHONY: lint test unit-test distro-test dashboard-check build clean docker-build

# Lint runs custom namedreturns linter followed by golangci-lint
lint:
	@echo "Running namedreturns linter..."
	namedreturns ./...
	@echo "Running golangci-lint..."
	golangci-lint run

# test is the single "run everything" target: unit tests AND distro renders.
test: unit-test dashboard-check distro-test

# unit-test runs the Go test suite
unit-test:
	go test -race -cover ./...

# dashboard-check validates the checked-in Grafana dashboard is well-formed JSON.
dashboard-check:
	@echo "Validating dashboard JSON..."
	@jq empty dashboards/diagnostic-bot.json \
		|| { echo "dashboards/diagnostic-bot.json is not valid JSON"; exit 1; }

# distro-test renders the Kustomize example and the Helm chart so a broken
# distribution fails the build like any other test.
distro-test:
	@echo "Rendering Kustomize example..."
	kustomize build kubernetes/ > /dev/null
	@echo "Linting Helm chart..."
	helm lint charts/diagnostic-bot
	@echo "Rendering Helm chart..."
	helm template diagnostic-bot charts/diagnostic-bot > /dev/null

# Build compiles the bot binary
build:
	mkdir -p bin
	go build -o bin/diagnostic-bot ./cmd/bot

# Clean removes build artifacts
clean:
	rm -rf bin/

# Docker build creates the container image
docker-build:
	docker build -t diagnostic-bot:latest .
