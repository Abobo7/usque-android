// Package usqueandroid provides Android-callable functions for the usque VPN library.
// This package is designed to be compiled with gomobile bind to produce an .aar file.
//
// Build with:
//
//	gomobile bind -v -target=android/arm64,android/arm -androidapi 24 -o usque.aar github.com/Diniboy1123/usque/android
package usqueandroid

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Diniboy1123/usque/api"
	"github.com/Diniboy1123/usque/config"
	"github.com/Diniboy1123/usque/internal"
)

// PacketFlow is the interface that Android must implement to exchange packets
// with the VPN TUN device. Go calls WritePacket for packets received from the
// Cloudflare tunnel.
type PacketFlow interface {
	WritePacket(data []byte)
}

// VpnStateCallback is the interface for VPN state notifications.
type VpnStateCallback interface {
	OnConnected()
	OnDisconnected(reason string)
	OnError(message string)
}

type tunnelState struct {
	mu        sync.Mutex
	running   bool
	connected bool
	cancel    context.CancelFunc
	callback  VpnStateCallback
	device    *AndroidTunDevice
	runID     uint64
}

var state = &tunnelState{}

var (
	optionsMu      sync.RWMutex
	customSNI      = "www.visa.cn" // Default SNI for censorship circumvention.
	customEndpoint = ""            // Optional endpoint, e.g. 162.159.198.2:443.
)

// Register creates a new Cloudflare WARP account and saves the configuration.
// This should be called once before starting the VPN.
func Register(configPath string, deviceName string) string {
	if err := config.LoadConfig(configPath); err == nil {
		return ""
	}

	accountData, err := api.Register(internal.DefaultModel, internal.DefaultLocale, "", true)
	if err != nil {
		return fmt.Sprintf("Registration failed: %v", err)
	}

	privKey, pubKey, err := internal.GenerateEcKeyPair()
	if err != nil {
		return fmt.Sprintf("Failed to generate key pair: %v", err)
	}

	updatedAccountData, err := api.EnrollKey(accountData.ID, accountData.Token, pubKey, deviceName)
	if err != nil {
		return fmt.Sprintf("Failed to enroll key: %v", err)
	}
	if len(updatedAccountData.Config.Peers) == 0 {
		return "Failed to enroll key: Cloudflare returned no tunnel peer"
	}

	peer := updatedAccountData.Config.Peers[0]
	endpointV4, err := normalizePeerEndpoint(peer.Endpoint.V4, false)
	if err != nil {
		return fmt.Sprintf("Failed to parse IPv4 endpoint: %v", err)
	}
	endpointV6, err := normalizePeerEndpoint(peer.Endpoint.V6, true)
	if err != nil {
		return fmt.Sprintf("Failed to parse IPv6 endpoint: %v", err)
	}

	config.AppConfig = config.Config{
		PrivateKey:     base64.StdEncoding.EncodeToString(privKey),
		EndpointV4:     endpointV4,
		EndpointV6:     endpointV6,
		EndpointH2V4:   config.DefaultEndpointH2V4,
		EndpointH2V6:   config.DefaultEndpointH2V6,
		EndpointPubKey: peer.PublicKey,
		ID:             updatedAccountData.ID,
		AccessToken:    accountData.Token,
		IPv4:           updatedAccountData.Config.Interface.Addresses.V4,
		IPv6:           updatedAccountData.Config.Interface.Addresses.V6,
	}

	if err := config.AppConfig.SaveConfig(configPath); err != nil {
		return fmt.Sprintf("Failed to save config: %v", err)
	}

	return ""
}

// normalizePeerEndpoint converts the API's endpoint notation to a literal IP
// without a port. Cloudflare currently returns IPv4 as address:port and IPv6
// as [address]:port, but this also handles plain addresses safely.
func normalizePeerEndpoint(raw string, optional bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && optional {
		return "", nil
	}

	host := raw
	if parsedHost, _, err := net.SplitHostPort(raw); err == nil {
		host = parsedHost
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = raw[1 : len(raw)-1]
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("invalid endpoint %q", raw)
	}
	return ip.String(), nil
}

// IsRegistered checks if a valid configuration exists.
func IsRegistered(configPath string) bool {
	return config.LoadConfig(configPath) == nil
}

// GetAssignedIPv4 returns the assigned IPv4 address from config.
func GetAssignedIPv4(configPath string) string {
	if err := config.LoadConfig(configPath); err != nil {
		return ""
	}
	return config.AppConfig.IPv4
}

// GetAssignedIPv6 returns the assigned IPv6 address from config.
func GetAssignedIPv6(configPath string) string {
	if err := config.LoadConfig(configPath); err != nil {
		return ""
	}
	return config.AppConfig.IPv6
}

// AndroidTunDevice wraps the Android TUN file descriptor for packet IO. It
// duplicates the descriptor on construction, so Go owns and closes its copy;
// Android remains responsible for the descriptor passed from ParcelFileDescriptor.
type AndroidTunDevice struct {
	file     *os.File
	outputFn PacketFlow
	writeMu  sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func newAndroidTunDevice(fd int, packetFlow PacketFlow) (*AndroidTunDevice, error) {
	if fd < 0 {
		return nil, fmt.Errorf("invalid TUN file descriptor %d", fd)
	}
	ownedFD, err := syscall.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("failed to duplicate TUN fd %d: %v", fd, err)
	}
	file := os.NewFile(uintptr(ownedFD), "android-tun")
	if file == nil {
		_ = syscall.Close(ownedFD)
		return nil, fmt.Errorf("failed to create file from fd %d", fd)
	}
	return &AndroidTunDevice{file: file, outputFn: packetFlow}, nil
}

func (d *AndroidTunDevice) ReadPacket(buf []byte) (int, error) {
	if d.file == nil {
		return 0, fmt.Errorf("TUN device is closed")
	}
	for {
		n, err := d.file.Read(buf)
		if err == nil || (!errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK)) {
			return n, err
		}
		// Android releases before Builder.setBlocking(true) expose a
		// non-blocking TUN fd. Avoid a tight spin while waiting for a packet;
		// closing the descriptor during shutdown breaks this loop promptly.
		time.Sleep(time.Millisecond)
	}
}

func (d *AndroidTunDevice) WritePacket(pkt []byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if d.outputFn != nil {
		d.outputFn.WritePacket(pkt)
		return nil
	}
	if d.file == nil {
		return fmt.Errorf("TUN device is closed")
	}
	_, err := d.file.Write(pkt)
	return err
}

func (d *AndroidTunDevice) Close() error {
	d.closeOnce.Do(func() {
		if d.file != nil {
			d.closeErr = d.file.Close()
		}
	})
	return d.closeErr
}

// StartTunnel starts the VPN tunnel using the provided TUN file descriptor.
// It returns only validation/setup errors; connection establishment and
// reconnects are reported through callback notifications.
func StartTunnel(configPath string, tunFd int, mtu int, packetFlow PacketFlow, callback VpnStateCallback) string {
	if mtu <= 0 || mtu > 65535 {
		return fmt.Sprintf("Invalid MTU: %d", mtu)
	}

	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return "Tunnel is already running"
	}

	if err := config.LoadConfig(configPath); err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to load config: %v", err)
	}

	privKey, err := config.AppConfig.GetEcPrivateKey()
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to get private key: %v", err)
	}
	peerPubKey, err := config.AppConfig.GetEcEndpointPublicKey()
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to get peer public key: %v", err)
	}

	cert, err := internal.GenerateCert(privKey, &privKey.PublicKey)
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to generate cert: %v", err)
	}

	optionsMu.RLock()
	sni := customSNI
	endpointOverride := customEndpoint
	optionsMu.RUnlock()
	if sni == "" {
		sni = internal.ConnectSNI
	}

	tlsConfig, err := api.PrepareTlsConfig(privKey, peerPubKey, cert, sni, false)
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to prepare TLS: %v", err)
	}

	var endpoint *net.UDPAddr
	if endpointOverride != "" {
		endpoint, err = parseEndpoint(endpointOverride)
	} else {
		var configuredEndpoint net.Addr
		configuredEndpoint, err = config.SelectEndpointFromConfig(false, false, 443)
		if err == nil {
			endpoint, _ = configuredEndpoint.(*net.UDPAddr)
		}
	}
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Invalid tunnel endpoint: %v", err)
	}
	if endpoint == nil {
		state.mu.Unlock()
		return "Invalid tunnel endpoint: expected UDP endpoint"
	}

	tunDevice, err := newAndroidTunDevice(tunFd, packetFlow)
	if err != nil {
		state.mu.Unlock()
		return fmt.Sprintf("Failed to create TUN device: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	state.runID++
	runID := state.runID
	state.cancel = cancel
	state.callback = callback
	state.device = tunDevice
	state.running = true
	state.connected = false
	state.mu.Unlock()

	log.Printf("Starting MASQUE tunnel: endpoint=%s, sni=%s, mtu=%d", endpoint, sni, mtu)
	go func() {
		defer func() { _ = tunDevice.Close() }()
		api.MaintainTunnel(ctx, api.MaintainTunnelConfig{
			TLSConfig:         tlsConfig,
			KeepalivePeriod:   30 * time.Second,
			InitialPacketSize: 1242,
			Endpoint:          endpoint,
			Device:            tunDevice,
			MTU:               mtu,
			ReconnectDelay:    time.Second,
			AlwaysReconnect:   true,
			UseHTTP2:          false,
			OnConnected: func() {
				state.mu.Lock()
				if !state.running || state.runID != runID {
					state.mu.Unlock()
					return
				}
				state.connected = true
				cb := state.callback
				state.mu.Unlock()
				if cb != nil {
					cb.OnConnected()
				}
			},
			OnConnectionLost: func() {
				state.mu.Lock()
				if !state.running || state.runID != runID {
					state.mu.Unlock()
					return
				}
				state.connected = false
				cb := state.callback
				state.mu.Unlock()
				if cb != nil {
					cb.OnDisconnected("Connection lost; retrying")
				}
			},
		})

		state.mu.Lock()
		if state.runID != runID {
			state.mu.Unlock()
			return
		}
		wasRunning := state.running
		cb := state.callback
		state.running = false
		state.connected = false
		state.cancel = nil
		state.callback = nil
		state.device = nil
		state.mu.Unlock()

		if wasRunning && cb != nil {
			cb.OnDisconnected("Tunnel stopped")
		}
		log.Println("MASQUE tunnel exited")
	}()

	return ""
}

// InputPacket is retained for binary compatibility with the original Android
// wrapper. Packet reads are now performed directly by the Go TUN reader, so
// callers must not use this method to feed packets.
func InputPacket(_ []byte) {}

// StopTunnel cancels the supervisor and closes Go's owned TUN duplicate so a
// blocked device read is woken immediately. Android closes its own descriptor
// copy as part of service cleanup.
func StopTunnel() {
	state.mu.Lock()
	if !state.running {
		state.mu.Unlock()
		return
	}

	cancel := state.cancel
	device := state.device
	state.running = false
	state.connected = false
	state.cancel = nil
	state.callback = nil
	state.device = nil
	state.mu.Unlock()

	log.Println("Stopping tunnel...")
	if cancel != nil {
		cancel()
	}
	if device != nil {
		_ = device.Close()
	}
}

// IsRunning returns true while the Go tunnel supervisor is active.
func IsRunning() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.running
}

// IsConnected reports whether the most recent Connect-IP handshake succeeded.
func IsConnected() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.connected
}

// GetVersion returns the Android wrapper version.
func GetVersion() string {
	return "1.0.4-android"
}

// parseEndpoint parses host[:port], [ipv6][:port], or a bare IP/hostname.
// The returned address is resolved once so MaintainTunnel always receives the
// concrete *net.UDPAddr it requires.
func parseEndpoint(raw string) (*net.UDPAddr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("endpoint is empty")
	}

	host := raw
	port := 443
	switch {
	case strings.HasPrefix(raw, "["):
		if h, p, err := net.SplitHostPort(raw); err == nil {
			host = h
			port, err = strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid port %q", p)
			}
		} else if strings.HasSuffix(raw, "]") {
			host = raw[1 : len(raw)-1]
		} else {
			return nil, fmt.Errorf("invalid bracketed endpoint %q", raw)
		}
	case strings.Count(raw, ":") == 1:
		h, p, err := net.SplitHostPort(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint %q: %v", raw, err)
		}
		host = h
		port, err = strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q", p)
		}
	case strings.Count(raw, ":") > 1:
		if net.ParseIP(raw) == nil {
			return nil, fmt.Errorf("IPv6 endpoints with a port must use [host]:port")
		}
	}

	if host == "" {
		return nil, fmt.Errorf("endpoint host is empty")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port %d is outside 1..65535", port)
	}

	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %v", host, err)
	}
	return addr, nil
}

// SetSNI sets a custom SNI for the TLS connection.
func SetSNI(sni string) {
	optionsMu.Lock()
	customSNI = strings.TrimSpace(sni)
	optionsMu.Unlock()
	log.Printf("SNI set to: %s", sni)
}

// GetSNI returns the current SNI setting.
func GetSNI() string {
	optionsMu.RLock()
	defer optionsMu.RUnlock()
	return customSNI
}

// SetEndpoint sets a custom MASQUE endpoint. Empty resets to the registered
// endpoint in config.json.
func SetEndpoint(endpoint string) {
	optionsMu.Lock()
	customEndpoint = strings.TrimSpace(endpoint)
	optionsMu.Unlock()
	log.Printf("Custom endpoint set to: %s", endpoint)
}

// GetEndpoint returns the current custom endpoint setting.
func GetEndpoint() string {
	optionsMu.RLock()
	defer optionsMu.RUnlock()
	return customEndpoint
}

// GetDefaultEndpoint returns the registered IPv4 endpoint with port 443.
func GetDefaultEndpoint(configPath string) string {
	if err := config.LoadConfig(configPath); err != nil {
		return ""
	}
	endpoint, err := config.SelectEndpointFromConfig(false, false, 443)
	if err != nil {
		return ""
	}
	return endpoint.String()
}

// ResetConnectionOptions resets all connection options to defaults.
func ResetConnectionOptions() {
	optionsMu.Lock()
	customSNI = "www.visa.cn"
	customEndpoint = ""
	optionsMu.Unlock()
	log.Println("Connection options reset to defaults")
}

// StartTunnelWithFd starts the tunnel by reading/writing directly to the TUN
// fd. The caller owns the fd and must close it after StopTunnel.
func StartTunnelWithFd(configPath string, tunFd int, callback VpnStateCallback) string {
	return StartTunnel(configPath, tunFd, 1280, nil, callback)
}

type fdReadWriter struct {
	file *os.File
}

func (f *fdReadWriter) Read(p []byte) (n int, err error) {
	return f.file.Read(p)
}

func (f *fdReadWriter) Write(p []byte) (n int, err error) {
	return f.file.Write(p)
}

// CreateTunReadWriter creates an io.ReadWriter from a TUN file descriptor.
func CreateTunReadWriter(fd int) io.ReadWriter {
	return &fdReadWriter{file: os.NewFile(uintptr(fd), "tun")}
}
