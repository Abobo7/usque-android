# Usque Android

Android implementation of the Usque VPN client using Cloudflare WARP/MASQUE protocol.

## Directory Structure

```
android/
└── usque-vpn/          # Android application project
    ├── app/            # Application module
    │   ├── libs/       # Place usque.aar here
    │   └── src/        # Kotlin source code
    ├── build.gradle.kts
    └── settings.gradle.kts
```

## Quick Start

### 1. Build Go Library (if not already built)

```bash
cd <project-root>/android
make init       # once per Go/Android toolchain
make android
```

### 2. Build Android App

```bash
cd usque-vpn
mkdir -p app/libs
cp ../usque.aar app/libs/

# Build
./gradlew assembleDebug
```

### 3. Install

```bash
adb install app/build/outputs/apk/debug/app-debug.apk
```

## Features

- ✅ Cloudflare WARP registration
- ✅ MASQUE/QUIC tunnel
- ✅ IPv4 + IPv6 dual stack
- ✅ Custom SNI configuration
- ✅ Custom endpoint configuration
- ✅ Persistent settings
- ✅ Foreground-service notification and Android 14 special-use declaration
- ✅ Automatic reconnect after a transient tunnel loss

## See Also

- [App README](usque-vpn/README.md) - Detailed app documentation
- [Main Project README](../README.md) - Overall project documentation
