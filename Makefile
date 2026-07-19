BINARY     := keywatch-agent
BUILD_DIR  := ./bin
CMD        := ./cmd/agent
MODULE     := github.com/keywatch/agent

.PHONY: all build test lint clean tidy install

all: tidy build test

## build: compile the agent binary to ./bin/keywatch-agent
build:
	@mkdir -p $(BUILD_DIR)
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "Built: $(BUILD_DIR)/$(BINARY)"

## test: run all unit tests
test:
	go test ./... -race -timeout 60s

## test-verbose: run tests with verbose output
test-verbose:
	go test ./... -race -v -timeout 60s

## lint: run go vet (add golangci-lint if installed)
lint:
	go vet ./...
	@if command -v golangci-lint &>/dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed — running go vet only"; \
	fi

## tidy: tidy and verify go.mod
tidy:
	go mod tidy
	go mod verify

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## install: build then run the install script (requires sudo)
install: build
	cp $(BUILD_DIR)/$(BINARY) ./$(BINARY)
	sudo bash scripts/install.sh
	rm -f ./$(BINARY)
