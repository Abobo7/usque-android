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
	customSNI        = "www.visa.cn" // Default SNI for censorship circumvention
	customEndpointV4 = ""            // Custom IPv4 endpoint (empty = use config)
	customEndpointV6 = ""            // Custom IPv6 endpoint (empty = use config)
	useIPv6          = false         // Whether to use IPv6 endpoint
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

	// Endpoint - use custom endpoint if set, otherwise use config
	var endpointIP string
	if useIPv6 {
		if customEndpointV6 != "" {
			endpointIP = customEndpointV6
		} else {
			endpointIP = config.AppConfig.EndpointV6
		}
	} else {
		if customEndpointV4 != "" {
			endpointIP = customEndpointV4
		} else {
			endpointIP = config.AppConfig.EndpointV4
		}
	}
	log.Printf("Using endpoint: %s", endpointIP)
	endpoint := &net.UDPAddr{
		IP:   net.ParseIP(endpointIP),
		Port: 443,
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
	return "1.0.1-android"
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

// SetEndpointV4 sets a custom IPv4 endpoint for the MASQUE connection.
// Pass empty string to use the endpoint from config.json.
// Example: "162.159.198.1"
func SetEndpointV4(endpoint string) {
	customEndpointV4 = endpoint
	log.Printf("Custom endpoint v4 set to: %s", endpoint)
}

// GetEndpointV4 returns the current IPv4 endpoint setting
func GetEndpointV4(configPath string) string {
	if customEndpointV4 != "" {
		return customEndpointV4
	}
	if err := config.LoadConfig(configPath); err == nil {
		return config.AppConfig.EndpointV4
	}
	return ""
}

// SetEndpointV6 sets a custom IPv6 endpoint for the MASQUE connection.
// Pass empty string to use the endpoint from config.json.
// Example: "2606:4700:103::"
func SetEndpointV6(endpoint string) {
	customEndpointV6 = endpoint
	log.Printf("Custom endpoint v6 set to: %s", endpoint)
}

// GetEndpointV6 returns the current IPv6 endpoint setting
func GetEndpointV6(configPath string) string {
	if customEndpointV6 != "" {
		return customEndpointV6
	}
	if err := config.LoadConfig(configPath); err == nil {
		return config.AppConfig.EndpointV6
	}
	return ""
}

// SetUseIPv6 sets whether to use IPv6 endpoint for connection.
// Default is false (use IPv4).
func SetUseIPv6(use bool) {
	useIPv6 = use
	log.Printf("UseIPv6 set to: %v", use)
}

// GetUseIPv6 returns whether IPv6 endpoint is being used
func GetUseIPv6() bool {
	return useIPv6
}

// ResetConnectionOptions resets all connection options to defaults
func ResetConnectionOptions() {
	customSNI = "www.visa.cn"
	customEndpointV4 = ""
	customEndpointV6 = ""
	useIPv6 = false
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
