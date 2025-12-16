# Usque VPN Example App

An example Android VPN application using the usque Go library.

## Prerequisites

1. Build the `usque.aar` library:
   ```bash
   cd ../
   make android
   ```

2. Copy the AAR to the libs folder:
   ```bash
   mkdir -p app/libs
   cp ../usque.aar app/libs/
   ```

## Building

Open the project in Android Studio or build from command line:

```bash
./gradlew assembleDebug
```

The APK will be at `app/build/outputs/apk/debug/app-debug.apk`

## How It Works

1. **Registration**: On first launch, the app registers with Cloudflare WARP to get credentials
2. **SOCKS5 Proxy**: The Go library starts a local SOCKS5 proxy connected to WARP
3. **VPN Service**: Android's VpnService creates a TUN interface
4. **Traffic Flow**: All device traffic → TUN → SOCKS5 Proxy → Cloudflare WARP

## Important Notes

### TUN to SOCKS Forwarding

The current `UsqueVpnService.kt` has a placeholder for TUN forwarding. For a production app, 
you need to implement proper packet forwarding or use a library like:

- [tun2socks](https://github.com/nicholasmhughes/tun2socks) - Java/native implementation
- [go-tun2socks](https://github.com/nicholasmhughes/go-tun2socks) - Go implementation

### Google Play Store

To publish on Google Play, you must:
1. Complete the VPN declaration form
2. Provide a video demonstration
3. Explain why your app needs VPN

See: https://support.google.com/googleplay/android-developer/answer/9214102

## License

MIT License
