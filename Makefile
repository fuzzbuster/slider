# Makefile

BINARY_NAME=slider
DIST_DIR=dist
PLATFORMS=linux/amd64 linux/386 darwin/amd64 darwin/arm64 windows/amd64 windows/386

all: build

build: $(PLATFORMS)

$(PLATFORMS):
	GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) go build -ldflags="-s -w" -trimpath -o $(DIST_DIR)/$(BINARY_NAME)-$(word 1,$(subst /, ,$@))-$(word 2,$(subst /, ,$@))-$$(date +%Y%m%d%H%M%S) ./main.go

clean:
	rm -rf $(DIST_DIR)/*

.PHONY: all build clean linux windows macos

linux:
	$(MAKE) build PLATFORMS="linux/amd64 linux/386"

windows:
	$(MAKE) build PLATFORMS="windows/amd64 windows/386"

macos:
	$(MAKE) build PLATFORMS="darwin/amd64 darwin/arm64"