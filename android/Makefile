.PHONY: android clean init help

GOMOBILE_VERSION ?= v0.0.0-20251209145715-2553ed8ce294
GOBIN ?= $(shell go env GOPATH)/bin
GOMOBILE ?= $(GOBIN)/gomobile

# Build AAR for Android
# Requires:
#   - Go version declared in ../go.mod
#   - gomobile (initialized with `make init`)
#   - Android NDK
#   - ANDROID_HOME or ANDROID_SDK_ROOT environment variable set

android:
	@echo "Building usque.aar for Android..."
	$(GOMOBILE) bind -v \
		-target=android/arm64,android/arm \
		-androidapi 24 \
		-ldflags="-s -w" \
		-o usque.aar \
		github.com/Diniboy1123/usque/android
	@echo "Build complete: usque.aar"

# Initialize gomobile (run once)
init:
	GOBIN=$(GOBIN) go install golang.org/x/mobile/cmd/gomobile@$(GOMOBILE_VERSION)
	$(GOMOBILE) init -v
	GOBIN=$(GOBIN) go install golang.org/x/mobile/cmd/gobind@$(GOMOBILE_VERSION)

clean:
	rm -f usque.aar usque-sources.jar

help:
	@echo "Usage:"
	@echo "  make init     - Install and initialize gomobile"
	@echo "  make android  - Build usque.aar for Android"
	@echo "  make clean    - Remove build artifacts"
