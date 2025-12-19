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
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Diniboy1123/usque/api"
	"github.com/Diniboy1123/usque/config"
	"github.com/Diniboy1123/usque/internal"
)

// PacketFlow is the interface that Android must implement to exchange packets with the VPN
// This interface is used for bidirectional packet flow between Android TUN and Go tunnel
type PacketFlow interface {
	// WritePacket writes an IP packet to the Android TUN device
	// Called by Go when a packet is received from Cloudflare
	WritePacket(data []byte)
}

// VpnStateCallback is the interface for VPN state notifications
type VpnStateCallback interface {
	// OnConnected is called when the VPN successfully connects to Cloudflare
	OnConnected()
	// OnDisconnected is called when the VPN disconnects
	OnDisconnected(reason string)
	// OnError is called when an error occurs
	OnError(message string)
}

// tunnelState holds the state of the running tunnel
type tunnelState struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	inputChan chan []byte
	callback  VpnStateCallback
}

var state = &tunnelState{}

// Custom connection options
var (
	customSNI      = "www.visa.cn" // Default SNI for censorship circumvention
	customEndpoint = ""            // Custom endpoint with port, e.g. "162.159.198.2:443" or "[2606:4700:103::]:1701"
)

// Register creates a new Cloudflare WARP account and saves the configuration.
// This should be called once before starting the VPN.
//
// Parameters:
//   - configPath: Absolute path where the config.json will be saved
//   - deviceName: Optional device name (can be empty)
//
// Returns:
//   - error string if registration fails, empty string on success
func Register(configPath string, deviceName string) string {
	// Already registered?
	if err := config.LoadConfig(configPath); err == nil {
		return "" // Config already exists and is valid
	}

	accountData, err := api.Register(internal.DefaultModel, internal.DefaultLocale, "", true)
	if err != nil {
		return fmt.Sprintf("Registration failed: %v", err)
	}

	privKey, pubKey, err := internal.GenerateEcKeyPair()
	if err != nil {
		return fmt.Sprintf("Failed to generate key pair: %v", err)
	}

	updatedAccountData, apiErr, err := api.EnrollKey(accountData, pubKey, deviceName)
	if err != nil {
		if apiErr != nil {
			return fmt.Sprintf("Failed to enroll key: %v (API: %s)", err, apiErr.ErrorsAsString("; "))
		}
		return fmt.Sprintf("Failed to enroll key: %v", err)
	}

	config.AppConfig = config.Config{
		PrivateKey:     base64.StdEncoding.EncodeToString(privKey),
		EndpointV4:     updatedAccountData.Config.Peers[0].Endpoint.V4[:len(updatedAccountData.Config.Peers[0].Endpoint.V4)-2],
		EndpointV6:     updatedAccountData.Config.Peers[0].Endpoint.V6[1 : len(updatedAccountData.Config.Peers[0].Endpoint.V6)-3],
		EndpointPubKey: updatedAccountData.Config.Peers[0].PublicKey,
		License:        updatedAccountData.Account.License,
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

// IsRegistered checks if a valid configuration exists
func IsRegistered(configPath string) bool {
	return config.LoadConfig(configPath) == nil
}

// GetAssignedIPv4 returns the assigned IPv4 address from config
func GetAssignedIPv4(configPath string) string {
	if err := config.LoadConfig(configPath); err != nil {
		return ""
	}
	return config.AppConfig.IPv4
}

// GetAssignedIPv6 returns the assigned IPv6 address from config
func GetAssignedIPv6(configPath string) string {
	if err := config.LoadConfig(configPath); err != nil {
		return ""
	}
	return config.AppConfig.IPv6
}

// AndroidTunDevice wraps the Android TUN file descriptor for packet IO
type AndroidTunDevice struct {
	fd       int
	file     *os.File
	mtu      int
	inputCh  chan []byte
	outputFn PacketFlow
}

// NewAndroidTunDevice creates a new Android TUN device wrapper
func newAndroidTunDevice(fd int, mtu int, packetFlow PacketFlow) (*AndroidTunDevice, error) {
	// Create a file from the file descriptor
	file := os.NewFile(uintptr(fd), "tun")
	if file == nil {
		return nil, fmt.Errorf("failed to create file from fd %d", fd)
	}

	return &AndroidTunDevice{
		fd:       fd,
		file:     file,
		mtu:      mtu,
		inputCh:  make(chan []byte, 256),
		outputFn: packetFlow,
	}, nil
}

func (d *AndroidTunDevice) ReadPacket(buf []byte) (int, error) {
	n, err := d.file.Read(buf)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (d *AndroidTunDevice) WritePacket(pkt []byte) error {
	if d.outputFn != nil {
		// Use the callback to write to Android TUN
		d.outputFn.WritePacket(pkt)
		return nil
	}
	// Fallback to direct write
	_, err := d.file.Write(pkt)
	return err
}

func (d *AndroidTunDevice) Close() error {
	if d.file != nil {
		return d.file.Close()
	}
	return nil
}

// StartTunnel starts the VPN tunnel using the provided TUN file descriptor.
// This function connects directly to Cloudflare WARP and forwards all traffic.
//
// Parameters:
//   - configPath: Path to the config.json file
//   - tunFd: The file descriptor of the Android TUN interface
//   - mtu: MTU size (usually 1280)
//   - packetFlow: Interface for writing packets back to Android TUN
//   - callback: State callback interface (can be nil)
//
// Returns:
//   - error string if startup fails, empty string on success
func StartTunnel(configPath string, tunFd int, mtu int, packetFlow PacketFlow, callback VpnStateCallback) string {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.running {
		return "Tunnel is already running"
	}

	log.Printf("StartTunnel called: configPath=%s, tunFd=%d, mtu=%d", configPath, tunFd, mtu)

	// Load config
	if err := config.LoadConfig(configPath); err != nil {
		return fmt.Sprintf("Failed to load config: %v", err)
	}

	// Get keys
	privKey, err := config.AppConfig.GetEcPrivateKey()
	if err != nil {
		return fmt.Sprintf("Failed to get private key: %v", err)
	}
	peerPubKey, err := config.AppConfig.GetEcEndpointPublicKey()
	if err != nil {
		return fmt.Sprintf("Failed to get peer public key: %v", err)
	}

	// Generate certificate
	cert, err := internal.GenerateCert(privKey, &privKey.PublicKey)
	if err != nil {
		return fmt.Sprintf("Failed to generate cert: %v", err)
	}

	// Prepare TLS config with custom SNI
	sni := customSNI
	if sni == "" {
		sni = internal.ConnectSNI
	}
	log.Printf("Using SNI: %s", sni)
	tlsConfig, err := api.PrepareTlsConfig(privKey, peerPubKey, cert, sni)
	if err != nil {
		return fmt.Sprintf("Failed to prepare TLS: %v", err)
	}

	// Create Android TUN device wrapper
	tunDevice, err := newAndroidTunDevice(tunFd, mtu, packetFlow)
	if err != nil {
		return fmt.Sprintf("Failed to create TUN device: %v", err)
	}

	// Endpoint - use custom endpoint if set, otherwise use config default
	var endpoint *net.UDPAddr
	if customEndpoint != "" {
		// Parse custom endpoint (supports host:port format)
		host, port, err := parseEndpoint(customEndpoint)
		if err != nil {
			return fmt.Sprintf("Invalid custom endpoint '%s': %v", customEndpoint, err)
		}
		endpoint = &net.UDPAddr{
			IP:   net.ParseIP(host),
			Port: port,
		}
		log.Printf("Using custom endpoint: %s:%d", host, port)
	} else {
		// Use default from config (IPv4)
		endpoint = &net.UDPAddr{
			IP:   net.ParseIP(config.AppConfig.EndpointV4),
			Port: 443,
		}
		log.Printf("Using default endpoint: %s:443", config.AppConfig.EndpointV4)
	}

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.running = true
	state.callback = callback

	// Start tunnel maintenance in background
	go func() {
		log.Println("Starting MASQUE tunnel...")

		// Notify connected after a brief delay for connection establishment
		go func() {
			time.Sleep(3 * time.Second)
			state.mu.Lock()
			running := state.running
			state.mu.Unlock()
			if running && callback != nil {
				callback.OnConnected()
			}
		}()

		api.MaintainTunnel(ctx, tlsConfig, 30*time.Second, 1242, endpoint, tunDevice, mtu, time.Second)

		// Tunnel exited
		log.Println("MASQUE tunnel exited")
		tunDevice.Close()

		state.mu.Lock()
		state.running = false
		state.mu.Unlock()

		if callback != nil {
			callback.OnDisconnected("Tunnel closed")
		}
	}()

	log.Println("Tunnel started successfully")
	return ""
}

// InputPacket sends an IP packet from Android TUN to the Go tunnel.
// This should be called by Android whenever a packet is read from the TUN device.
//
// Parameters:
//   - data: The raw IP packet bytes
func InputPacket(data []byte) {
	state.mu.Lock()
	ch := state.inputChan
	state.mu.Unlock()

	if ch != nil {
		// Non-blocking send
		select {
		case ch <- data:
		default:
			// Channel full, drop packet
		}
	}
}

// StopTunnel stops the running tunnel
func StopTunnel() {
	state.mu.Lock()
	defer state.mu.Unlock()

	if !state.running {
		return
	}

	log.Println("Stopping tunnel...")

	if state.cancel != nil {
		state.cancel()
	}

	state.running = false
}

// IsRunning returns true if the tunnel is currently running
func IsRunning() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.running
}

// GetVersion returns the library version
func GetVersion() string {
	return "1.0.3-android"
}

// parseEndpoint parses an endpoint string in the format:
// - "host:port" for IPv4 (e.g., "162.159.198.2:443")
// - "[host]:port" for IPv6 (e.g., "[2606:4700:103::]:1701")
// - "host" without port (defaults to 443)
func parseEndpoint(endpoint string) (string, int, error) {
	// Check if it's an IPv6 address with brackets
	if len(endpoint) > 0 && endpoint[0] == '[' {
		// IPv6 format: [host]:port
		closeBracket := -1
		for i, c := range endpoint {
			if c == ']' {
				closeBracket = i
				break
			}
		}
		if closeBracket == -1 {
			return "", 0, fmt.Errorf("missing closing bracket for IPv6 address")
		}

		host := endpoint[1:closeBracket]

		// Check for port after bracket
		if closeBracket+1 < len(endpoint) && endpoint[closeBracket+1] == ':' {
			portStr := endpoint[closeBracket+2:]
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return "", 0, fmt.Errorf("invalid port: %s", portStr)
			}
			return host, port, nil
		}

		// No port, use default
		return host, 443, nil
	}

	// IPv4 or hostname format
	lastColon := -1
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			lastColon = i
			break
		}
	}

	if lastColon != -1 {
		// Has port
		host := endpoint[:lastColon]
		portStr := endpoint[lastColon+1:]
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %s", portStr)
		}
		return host, port, nil
	}

	// No port, use default
	return endpoint, 443, nil
}

// ============================================
// Connection Configuration Functions
// ============================================

// SetSNI sets a custom SNI for the TLS connection.
// This can help with censorship circumvention.
// Default is "www.visa.cn". Pass empty string to use Cloudflare's default.
func SetSNI(sni string) {
	customSNI = sni
	log.Printf("SNI set to: %s", sni)
}

// GetSNI returns the current SNI setting
func GetSNI() string {
	return customSNI
}

// SetEndpoint sets a custom endpoint for the MASQUE connection.
// Supports the following formats:
//   - "162.159.198.2" (IPv4, default port 443)
//   - "162.159.198.2:1701" (IPv4 with custom port)
//   - "[2606:4700:103::]" (IPv6, default port 443)
//   - "[2606:4700:103::]:1701" (IPv6 with custom port)
//
// Pass empty string to use the default endpoint from config.json.
func SetEndpoint(endpoint string) {
	customEndpoint = endpoint
	log.Printf("Custom endpoint set to: %s", endpoint)
}

// GetEndpoint returns the current custom endpoint setting
func GetEndpoint() string {
	return customEndpoint
}

// GetDefaultEndpoint returns the default endpoint from config (IPv4:443)
func GetDefaultEndpoint(configPath string) string {
	if err := config.LoadConfig(configPath); err == nil {
		return config.AppConfig.EndpointV4 + ":443"
	}
	return ""
}

// ResetConnectionOptions resets all connection options to defaults
func ResetConnectionOptions() {
	customSNI = "www.visa.cn"
	customEndpoint = ""
	log.Println("Connection options reset to defaults")
}

// ============================================
// Alternative: File Descriptor based approach
// ============================================

// StartTunnelWithFd starts the tunnel by reading/writing directly to the TUN fd.
// This is simpler but requires the TUN fd to be readable/writable from Go.
func StartTunnelWithFd(configPath string, tunFd int, callback VpnStateCallback) string {
	return StartTunnel(configPath, tunFd, 1280, nil, callback)
}

// fdReadWriter wraps a file descriptor for io.ReadWriter
type fdReadWriter struct {
	file *os.File
}

func (f *fdReadWriter) Read(p []byte) (n int, err error) {
	return f.file.Read(p)
}

func (f *fdReadWriter) Write(p []byte) (n int, err error) {
	return f.file.Write(p)
}

// CreateTunReadWriter creates an io.ReadWriter from a TUN file descriptor
func CreateTunReadWriter(fd int) io.ReadWriter {
	file := os.NewFile(uintptr(fd), "tun")
	return &fdReadWriter{file: file}
}
