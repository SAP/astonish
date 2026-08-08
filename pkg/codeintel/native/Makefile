SHELL := /bin/bash

TREE_SITTER_VERSION ?= v0.25.10
TREE_SITTER_GO_VERSION ?= v0.23.4
TREE_SITTER_TYPESCRIPT_VERSION ?= v0.23.2
TREE_SITTER_JAVASCRIPT_VERSION ?= v0.23.1
TREE_SITTER_PYTHON_VERSION ?= v0.23.6

BUILD_DIR ?= $(CURDIR)/.build
OUT_DIR ?= $(CURDIR)/dist
OUT ?= $(OUT_DIR)/libastonish-treesitter.so
OUT_PARENT := $(dir $(OUT))
CC ?= cc
CFLAGS ?= -O2 -fPIC

EMBED_DIR ?= $(CURDIR)/embed
EMBED_TARBALL ?= $(EMBED_DIR)/treesitter-src.tar.gz

.PHONY: all clean deps embed

all: $(OUT)

$(OUT): deps
	@mkdir -p $(OUT_PARENT)
	$(CC) $(CFLAGS) -shared \
		-I$(BUILD_DIR)/tree-sitter/lib/include \
		-I$(BUILD_DIR)/tree-sitter/lib/src \
		-I$(BUILD_DIR)/tree-sitter-go/src \
		-I$(BUILD_DIR)/tree-sitter-typescript/typescript/src \
		-I$(BUILD_DIR)/tree-sitter-typescript/tsx/src \
		-I$(BUILD_DIR)/tree-sitter-javascript/src \
		-I$(BUILD_DIR)/tree-sitter-python/src \
		$(BUILD_DIR)/tree-sitter/lib/src/lib.c \
		$(BUILD_DIR)/tree-sitter-go/src/parser.c \
		$(BUILD_DIR)/tree-sitter-typescript/typescript/src/parser.c \
		$(BUILD_DIR)/tree-sitter-typescript/typescript/src/scanner.c \
		$(BUILD_DIR)/tree-sitter-typescript/tsx/src/parser.c \
		$(BUILD_DIR)/tree-sitter-typescript/tsx/src/scanner.c \
		$(BUILD_DIR)/tree-sitter-javascript/src/parser.c \
		$(BUILD_DIR)/tree-sitter-javascript/src/scanner.c \
		$(BUILD_DIR)/tree-sitter-python/src/parser.c \
		$(BUILD_DIR)/tree-sitter-python/src/scanner.c \
		-o $(OUT)

deps:
	@mkdir -p $(BUILD_DIR)
	@test -d $(BUILD_DIR)/tree-sitter || git clone --depth 1 --branch $(TREE_SITTER_VERSION) https://github.com/tree-sitter/tree-sitter $(BUILD_DIR)/tree-sitter
	@test -d $(BUILD_DIR)/tree-sitter-go || git clone --depth 1 --branch $(TREE_SITTER_GO_VERSION) https://github.com/tree-sitter/tree-sitter-go $(BUILD_DIR)/tree-sitter-go
	@test -d $(BUILD_DIR)/tree-sitter-typescript || git clone --depth 1 --branch $(TREE_SITTER_TYPESCRIPT_VERSION) https://github.com/tree-sitter/tree-sitter-typescript $(BUILD_DIR)/tree-sitter-typescript
	@test -d $(BUILD_DIR)/tree-sitter-javascript || git clone --depth 1 --branch $(TREE_SITTER_JAVASCRIPT_VERSION) https://github.com/tree-sitter/tree-sitter-javascript $(BUILD_DIR)/tree-sitter-javascript
	@test -d $(BUILD_DIR)/tree-sitter-python || git clone --depth 1 --branch $(TREE_SITTER_PYTHON_VERSION) https://github.com/tree-sitter/tree-sitter-python $(BUILD_DIR)/tree-sitter-python

clean:
	rm -rf $(BUILD_DIR) $(OUT_DIR)

# Regenerate the embedded C source tarball that local code mode compiles at
# runtime (see builder.go). Includes only the sources the compile manifest in
# embed/embed.go needs — note tree-sitter-typescript/common is required by the
# TS/TSX scanners. Run after bumping any grammar version above, then bump
# embed.Version to match.
embed: deps
	@mkdir -p $(EMBED_DIR)
	tar czf $(EMBED_TARBALL) -C $(BUILD_DIR) \
		tree-sitter/lib/include tree-sitter/lib/src \
		tree-sitter-go/src \
		tree-sitter-typescript/common \
		tree-sitter-typescript/typescript/src \
		tree-sitter-typescript/tsx/src \
		tree-sitter-javascript/src \
		tree-sitter-python/src
	@echo "Wrote $(EMBED_TARBALL) ($$(du -h $(EMBED_TARBALL) | cut -f1))"
