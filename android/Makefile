.PHONY: android clean

# Build AAR for Android
# Requires:
#   - Go 1.24.1+
#   - gomobile: go install golang.org/x/mobile/cmd/gomobile@latest
#   - Android NDK
#   - ANDROID_HOME environment variable set

android:
	@echo "Building usque.aar for Android..."
	gomobile bind -v \
		-target=android/arm64,android/arm \
		-androidapi 24 \
		-ldflags="-s -w" \
		-o usque.aar \
		github.com/Diniboy1123/usque/android
	@echo "Build complete: usque.aar"

# Initialize gomobile (run once)
init:
	go install golang.org/x/mobile/cmd/gomobile@latest
	gomobile init -v

clean:
	rm -f usque.aar usque-sources.jar

help:
	@echo "Usage:"
	@echo "  make init     - Install and initialize gomobile"
	@echo "  make android  - Build usque.aar for Android"
	@echo "  make clean    - Remove build artifacts"
