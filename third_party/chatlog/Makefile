BINARY_NAME := chatlog
BIN := bin/$(BINARY_NAME)
GO := go
GO_TAGS := -tags sqlite_fts5
RUN_ARGS ?=
BUILD_SCRIPT := scripts/build_chatlog_binary.sh
VERIFY_SCRIPT := scripts/verify_chatlog_binary.sh
GO_SOURCES := $(shell find . -type f -name '*.go' -not -path './.git/*')
EMBEDDED_ASSETS := $(shell find internal -type f \( -name '*.htm' -o -name '*.html' -o -name '*.css' -o -name '*.js' -o -name '*.py' -o -name '*.wasm' -o -name '*.sh' -o -name '*.yaml' -o -name '*.yml' \))
ifeq ($(VERSION),)
	VERSION := $(shell git describe --tags --always --dirty="-dev" 2>/dev/null || echo "unknown")
endif

PLATFORMS := \
	darwin/arm64

UPX_PLATFORMS :=

.PHONY: all clean lint tidy test build run crossbuild upx

all: clean lint tidy test build

clean:
	@echo "🧹 Cleaning..."
	@rm -rf bin/

lint:
	@echo "🕵️‍♂️ Running linters..."
	golangci-lint run --build-tags sqlite_fts5 ./...

tidy:
	@echo "🧼 Tidying up dependencies..."
	$(GO) mod tidy

test:
	@echo "🧪 Running tests..."
	CGO_ENABLED=1 $(GO) test $(GO_TAGS) ./... -cover

build: $(BIN)

$(BIN): $(GO_SOURCES) $(EMBEDDED_ASSETS) go.mod go.sum $(BUILD_SCRIPT) $(VERIFY_SCRIPT)
	@echo "🔨 Building for current platform..."
	@CHATLOG_VERSION="$(VERSION)" $(BUILD_SCRIPT) $(BIN)

run: build
	@echo "🚀 Running compiled binary with SQLite FTS5..."
	@exec ./$(BIN) $(RUN_ARGS)

crossbuild: clean
	@echo "🌍 Building for multiple platforms..."
	for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d/ -f1); \
		arch=$$(echo $$platform | cut -d/ -f2); \
		float=$$(echo $$platform | cut -d/ -f3); \
		output_name=bin/chatlog_$${os}_$${arch}; \
		[ "$$float" != "" ] && output_name=$$output_name_$$float; \
		echo "🔨 Building for $$os/$$arch..."; \
		echo "🔨 Building for $$output_name..."; \
		CHATLOG_VERSION="$(VERSION)" GOOS=$$os GOARCH=$$arch GOARM=$$float $(BUILD_SCRIPT) $$output_name ; \
		if [ "$(ENABLE_UPX)" = "1" ] && echo "$(UPX_PLATFORMS)" | grep -q "$$os/$$arch"; then \
			echo "⚙️ Compressing binary $$output_name..." && upx --best $$output_name; \
		fi; \
	done
