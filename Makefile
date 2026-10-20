.PHONY: all public extension test fmt vet check clean antlr-public antlr-ext

APP_NAME := urphin
BUILD_DIR := build

all: check public

# Public generation needs code-state .g4; extension generation needs real names.
antlr-public:
	./antlr_generate.sh public

antlr-ext:
	@test -d extension || (echo "Error: extension submodule is missing"; exit 1)
	./antlr_generate.sh extension

# Build without the extensions submodule
public: antlr-public
	mkdir -p $(BUILD_DIR)
	go build -tags public -o $(BUILD_DIR)/$(APP_NAME) ./tui

# Build with the extensions submodule
extension: antlr-ext
	mkdir -p $(BUILD_DIR)
	go build -tags extension -o $(BUILD_DIR)/$(APP_NAME)-ext ./tui

# Run all tests
test:
	go test $$(go list ./... | grep -v '/extension/')
	@if [ -d extension ] && [ -n "$$(ls -A extension)" ]; then go test -tags extension ./extension/...; fi

# Format Go source files
fmt:
	gofmt -w $$(find . -type f -name '*.go' -not -path './.git/*' -not -path '*/generated/*')

# Check formatting without modifying files
fmt-check:
	@test -z "$$(gofmt -l $$(find . -type f -name '*.go' -not -path './.git/*' -not -path '*/generated/*'))" \
		|| (echo "Go files need formatting; run 'make fmt'"; exit 1)

# Static analysis
vet:
	go vet $$(go list ./... | grep -v /generated)

# Common development checks
check: fmt-check test vet

# Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

