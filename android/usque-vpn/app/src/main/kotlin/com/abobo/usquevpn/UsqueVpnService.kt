package com.abobo.usquevpn

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import usqueandroid.PacketFlow
import usqueandroid.Usqueandroid
import usqueandroid.VpnStateCallback
import java.io.FileOutputStream
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.Future

/**
 * Owns the Android TUN interface and the Go MASQUE supervisor.
 *
 * Startup is deliberately asynchronous: Cloudflare registration and key
 * enrollment are network operations and must never run on the service main
 * thread. The service enters the foreground before doing that work so Android
 * does not kill it while the VPN is being initialized.
 */
class UsqueVpnService : VpnService() {

    companion object {
        private const val TAG = "UsqueVpnService"
        private const val CHANNEL_ID = "usque_vpn"
        private const val NOTIFICATION_ID = 1001
        private const val HANDSHAKE_TIMEOUT_MS = 15_000L
        const val ACTION_DISCONNECT = "com.abobo.usquevpn.DISCONNECT"

        @Volatile
        var isRunning = false
            private set

        @Volatile
        var isConnected = false
            private set

        private var instance: UsqueVpnService? = null

        fun stop() {
            Log.i(TAG, "Static stop() called")
            instance?.disconnect()
        }
    }

    private val lifecycleLock = Any()
    private val mainHandler = Handler(Looper.getMainLooper())
    private val startupExecutor: ExecutorService = Executors.newSingleThreadExecutor()
    private var startupTask: Future<*>? = null
    private var stopRequested = false
    @Volatile
    private var hasConnected = false

    private var vpnInterface: ParcelFileDescriptor? = null
    private var goTunInterface: ParcelFileDescriptor? = null
    private var outputStream: FileOutputStream? = null
    private val packetWriteLock = Any()

    override fun onCreate() {
        super.onCreate()
        instance = this
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            Log.i(TAG, "Received disconnect intent")
            disconnect()
            return START_NOT_STICKY
        }

        synchronized(lifecycleLock) {
            if (isRunning) {
                Log.w(TAG, "VPN already running")
                return START_STICKY
            }

            stopRequested = false
            hasConnected = false
            isRunning = true
            isConnected = false
            try {
                startForegroundWithStatus("Connecting…")
            } catch (e: Exception) {
                Log.e(TAG, "Unable to enter foreground", e)
                isRunning = false
                stopSelf()
                return START_NOT_STICKY
            }

            startupTask = startupExecutor.submit {
                try {
                    startVpnInternal()
                } finally {
                    synchronized(lifecycleLock) {
                        startupTask = null
                    }
                }
            }
        }

        return START_STICKY
    }

    private fun startVpnInternal() {
        val configPath = "${filesDir.absolutePath}/config.json"

        try {
            if (shouldStop()) return

            if (!Usqueandroid.isRegistered(configPath)) {
                Log.i(TAG, "Not registered, registering now…")
                val error = Usqueandroid.register(configPath, Build.MODEL)
                if (error.isNotEmpty()) {
                    failStart("Registration failed: $error")
                    return
                }
                Log.i(TAG, "Registration successful")
            }

            if (shouldStop()) return

            val vpnIpv4 = Usqueandroid.getAssignedIPv4(configPath)
            val vpnIpv6 = Usqueandroid.getAssignedIPv6(configPath)
            Log.i(TAG, "Assigned IPs: v4=$vpnIpv4, v6=$vpnIpv6")

            if (vpnIpv4.isEmpty()) {
                failStart("No IPv4 address assigned")
                return
            }

            val builder = Builder()
                .setSession("Usque WARP VPN")
                .setMtu(1280)
                .addAddress(vpnIpv4, 32)
                .addRoute("0.0.0.0", 0)
                .addDnsServer("1.1.1.1")
                .addDnsServer("1.0.0.1")
                // The Go MASQUE socket must stay outside the VPN it carries.
                .addDisallowedApplication(packageName)

            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                // VpnService fds are non-blocking by default on newer Android.
                // Go's packet reader is intentionally also tolerant of EAGAIN
                // for older releases where this API is unavailable.
                builder.setBlocking(true)
            }

            if (vpnIpv6.isNotEmpty()) {
                try {
                    builder.addAddress(vpnIpv6, 128)
                    builder.addRoute("::", 0)
                    builder.addDnsServer("2606:4700:4700::1111")
                    builder.addDnsServer("2606:4700:4700::1001")
                    Log.i(TAG, "IPv6 configured: $vpnIpv6")
                } catch (e: Exception) {
                    Log.w(TAG, "Failed to add IPv6; continuing with IPv4 only", e)
                }
            }

            val establishedInterface = builder.establish()
            if (establishedInterface == null) {
                failStart("Failed to establish VPN interface")
                return
            }

            synchronized(lifecycleLock) {
                if (stopRequested) {
                    establishedInterface.close()
                    return
                }
                vpnInterface = establishedInterface
            }

            // Keep three independent fd owners: the service's original fd,
            // a Go/read fd, and a Kotlin/write fd. This avoids double-closing
            // one numeric fd while still letting disconnect wake a blocked Go
            // read immediately.
            val goDescriptor = ParcelFileDescriptor.dup(establishedInterface.fileDescriptor)
            val writeDescriptor = ParcelFileDescriptor.dup(establishedInterface.fileDescriptor)
            val writeStream = ParcelFileDescriptor.AutoCloseOutputStream(writeDescriptor)

            synchronized(lifecycleLock) {
                if (stopRequested) {
                    goDescriptor.close()
                    writeStream.close()
                    return
                }
                goTunInterface = goDescriptor
                outputStream = writeStream
            }

            val packetFlow = object : PacketFlow {
                override fun writePacket(data: ByteArray?) {
                    if (data == null || data.isEmpty()) return
                    synchronized(packetWriteLock) {
                        try {
                            outputStream?.write(data)
                        } catch (e: Exception) {
                            if (!shouldStop()) {
                                Log.e(TAG, "Failed to write packet to TUN", e)
                            }
                        }
                    }
                }
            }

            val callback = object : VpnStateCallback {
                override fun onConnected() {
                    if (shouldStop()) return
                    hasConnected = true
                    mainHandler.removeCallbacks(handshakeTimeout)
                    isConnected = true
                    Log.i(TAG, "MASQUE tunnel connected to Cloudflare")
                    updateForegroundStatus("Connected")
                }

                override fun onDisconnected(reason: String?) {
                    isConnected = false
                    Log.w(TAG, "MASQUE tunnel disconnected: $reason")
                    if (reason == "Tunnel stopped" && !shouldStop()) {
                        synchronized(lifecycleLock) {
                            isRunning = false
                            stopRequested = true
                        }
                        closeResources()
                        stopForeground(true)
                        stopSelf()
                        return
                    }
                    if (!shouldStop()) {
                        // The Go supervisor reconnects automatically. Keep the
                        // TUN alive so traffic resumes after the next handshake.
                        updateForegroundStatus("Reconnecting…")
                    }
                }

                override fun onError(message: String?) {
                    Log.e(TAG, "MASQUE tunnel error: $message")
                    updateForegroundStatus("Error")
                }
            }

            val goFd = synchronized(lifecycleLock) { goTunInterface?.fd ?: -1 }
            if (goFd < 0 || shouldStop()) return

            val tunnelError = Usqueandroid.startTunnel(
                configPath,
                goFd.toLong(),
                1280,
                packetFlow,
                callback
            )
            if (tunnelError.isNotEmpty()) {
                failStart("Failed to start tunnel: $tunnelError")
                return
            }

            Log.i(TAG, "VPN service started; waiting for MASQUE handshake")
            updateForegroundStatus("Connecting…")
            mainHandler.postDelayed(handshakeTimeout, HANDSHAKE_TIMEOUT_MS)
        } catch (e: Exception) {
            if (!shouldStop()) {
                failStart("Failed to start VPN: ${e.message ?: e.javaClass.simpleName}")
            }
        }
    }

    private fun shouldStop(): Boolean = synchronized(lifecycleLock) { stopRequested }

    private fun failStart(message: String) {
        Log.e(TAG, message)
        mainHandler.removeCallbacks(handshakeTimeout)
        hasConnected = false
        synchronized(lifecycleLock) {
            stopRequested = true
            isRunning = false
            isConnected = false
        }
        closeResources()
        stopForeground(true)
        stopSelf()
    }

    /** Stops the Go supervisor, wakes its TUN reader, then releases all fd owners. */
    fun disconnect() {
        mainHandler.removeCallbacks(handshakeTimeout)
        synchronized(lifecycleLock) {
            if (!isRunning && startupTask == null && vpnInterface == null && goTunInterface == null) {
                return
            }
            stopRequested = true
            isRunning = false
            isConnected = false
            startupTask?.cancel(true)
        }

        try {
            Usqueandroid.stopTunnel()
        } catch (e: Exception) {
            Log.e(TAG, "Error stopping Go tunnel", e)
        }

        closeResources()
        stopForeground(true)
        stopSelf()
    }

    private val handshakeTimeout = Runnable {
        if (isRunning && !hasConnected) {
            failStart("MASQUE handshake timed out after ${HANDSHAKE_TIMEOUT_MS / 1000}s")
        }
    }

    private fun closeResources() {
        val goDescriptor: ParcelFileDescriptor?
        val stream: FileOutputStream?
        val vpn: ParcelFileDescriptor?
        synchronized(lifecycleLock) {
            goDescriptor = goTunInterface
            stream = outputStream
            vpn = vpnInterface
            goTunInterface = null
            outputStream = null
            vpnInterface = null
        }

        // StopTunnel closes Go's own dup of this descriptor; close the Kotlin
        // copy as well so no ParcelFileDescriptor remains live after stop.
        try {
            goDescriptor?.close()
        } catch (e: Exception) {
            Log.w(TAG, "Error closing Go TUN descriptor", e)
        }
        try {
            stream?.close()
        } catch (e: Exception) {
            Log.w(TAG, "Error closing TUN output", e)
        }
        try {
            vpn?.close()
        } catch (e: Exception) {
            Log.w(TAG, "Error closing VPN interface", e)
        }
    }

    override fun onDestroy() {
        Log.i(TAG, "onDestroy() called")
        disconnect()
        startupExecutor.shutdownNow()
        instance = null
        super.onDestroy()
    }

    override fun onRevoke() {
        Log.i(TAG, "VPN revoked by user")
        disconnect()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "Usque VPN",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Usque MASQUE VPN status"
            }
            getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        }
    }

    private fun buildNotification(status: String) = NotificationCompat.Builder(this, CHANNEL_ID)
        .setSmallIcon(R.drawable.ic_vpn)
        .setContentTitle(getString(R.string.app_name))
        .setContentText(status)
        .setOngoing(true)
        .setCategory(NotificationCompat.CATEGORY_SERVICE)
        .setContentIntent(createLaunchPendingIntent())
        .build()

    private fun createLaunchPendingIntent(): PendingIntent? {
        val launchIntent = packageManager.getLaunchIntentForPackage(packageName) ?: return null
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                PendingIntent.FLAG_IMMUTABLE
            } else {
                0
            }
        return PendingIntent.getActivity(this, 0, launchIntent, flags)
    }

    private fun startForegroundWithStatus(status: String) {
        val notification = buildNotification(status)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun updateForegroundStatus(status: String) {
        if (!isRunning) return
        getSystemService(NotificationManager::class.java)
            .notify(NOTIFICATION_ID, buildNotification(status))
    }
}
