# Usque VPN for Android

A native Android VPN application powered by Cloudflare WARP using the MASQUE protocol.

## Features

- 🔒 **Secure VPN** - Uses Cloudflare WARP infrastructure
- 🌐 **Dual Stack** - Full IPv4 and IPv6 support
- 🔧 **Customizable** - Configure SNI and endpoints for censorship circumvention
- 📱 **Native Android** - Built with Kotlin and Android VpnService
- ⚡ **Fast** - MASQUE/QUIC protocol for optimal performance

## Prerequisites

1. **Build the Go library first:**
   ```bash
   cd ../
   make android
   ```

2. **Copy the AAR to libs:**
   ```bash
   mkdir -p app/libs
   cp ../usque.aar app/libs/
   ```

3. **Android Studio** or Gradle 8.5+

## Building

### From Android Studio
1. Open this directory in Android Studio
2. Copy `../usque.aar` to `app/libs/usque.aar`
3. Sync Gradle
4. Build → Build APK

### From Command Line
```bash
./gradlew assembleDebug
# or
./gradlew assembleRelease
```

Output: `app/build/outputs/apk/debug/app-debug.apk`

## Usage

1. **First Connect**: The service registers with Cloudflare WARP off the main thread
2. **Connect**: Approve Android's VPN permission and tap the "Connect" button
3. **Status**: `Connecting` is shown until the Connect-IP handshake succeeds
4. **Settings**: Configure SNI and an endpoint before connecting

### Settings Options

| Option | Description | Default |
|--------|-------------|---------|
| SNI | Server Name Indication for TLS | `www.visa.cn` |
| Endpoint | IP/hostname with optional port; bracket IPv6 when a port is supplied | From registration |

The default endpoint is the registered IPv4 address. A custom IPv6 endpoint can
be entered as `[2001:db8::1]:443`; a hostname is resolved before the tunnel
supervisor starts.

## Architecture

```
┌─────────────────────────────────────────┐
│           Android App (Kotlin)          │
├─────────────────────────────────────────┤
│  MainActivity     │  UsqueVpnService    │
│  - Settings UI    │  - TUN interface    │
│  - Connect/Stop   │  - VPN lifecycle    │
└─────────┬─────────┴──────────┬──────────┘
          │                    │
          │    usque.aar       │
          │   (Go Library)     │
          ▼                    ▼
┌─────────────────────────────────────────┐
│         usqueandroid Package            │
│  - StartTunnel()  - SetSNI()            │
│  - StopTunnel()   - SetEndpoint()       │
│  - Register()     - IsConnected()       │
└─────────────────┬───────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────┐
│         Cloudflare WARP Network         │
│            (MASQUE/QUIC)                │
└─────────────────────────────────────────┘
```

## Project Structure

```
usque-vpn/
├── app/
│   ├── libs/                    # usque.aar goes here
│   ├── src/main/
│   │   ├── kotlin/.../
│   │   │   ├── MainActivity.kt      # Main UI
│   │   │   └── UsqueVpnService.kt   # VPN service
│   │   ├── res/                 # Layouts, drawables
│   │   └── AndroidManifest.xml
│   └── build.gradle.kts
├── build.gradle.kts
├── settings.gradle.kts
└── README.md
```

## Troubleshooting

### VPN won't connect
- Check if another VPN is active
- Verify internet connection
- Try changing the endpoint

### No IPv6
- Ensure your network supports IPv6
- Check if IPv6 address was assigned in registration

### Settings reset after restart
- Settings are saved in SharedPreferences
- The WARP config (private key and access token) is stored in app-internal
  storage with mode `0600` and Android backup is disabled

## License

MIT License - See [LICENSE](../LICENSE.md)
