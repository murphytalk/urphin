.PHONY: all public extention test fmt vet check clean

APP_NAME := urphin
BUILD_DIR := build

all: check public

# Build without the extensions submodule
public:
	mkdir -p $(BUILD_DIR)
	go build -tags public -o $(BUILD_DIR)/$(APP_NAME) ./tui

# Build with the extensions submodule
extension:
	@test -d extension || (echo "Error: extension submodule is missing"; exit 1)
	mkdir -p $(BUILD_DIR)
	go build -tags extension -o $(BUILD_DIR)/$(APP_NAME)-ext ./tui

# Run all tests
test:
	go test ./...

# Format Go source files
fmt:
	gofmt -w $$(find . -type f -name '*.go' -not -path './.git/*')

# Check formatting without modifying files
fmt-check:
	@test -z "$$(gofmt -l $$(find . -type f -name '*.go' -not -path './.git/*'))" \
		|| (echo "Go files need formatting; run 'make fmt'"; exit 1)

# Static analysis
vet:
	go vet ./...

# Common development checks
check: fmt-check test vet

# Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

