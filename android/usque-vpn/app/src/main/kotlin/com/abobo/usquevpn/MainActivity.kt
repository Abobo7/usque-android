package com.abobo.usquevpn

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat
import usqueandroid.Usqueandroid

class MainActivity : Activity() {

    companion object {
        private const val VPN_REQUEST_CODE = 1001
        private const val NOTIFICATION_REQUEST_CODE = 1002
        private const val PREFS_NAME = "UsqueVpnPrefs"
        private const val KEY_SNI = "sni"
        private const val KEY_ENDPOINT = "endpoint"
    }

    private lateinit var prefs: SharedPreferences
    private lateinit var statusText: TextView
    private lateinit var connectButton: Button
    private lateinit var ipInfoText: TextView
    private lateinit var settingsButton: Button
    private lateinit var sniText: TextView
    private lateinit var endpointText: TextView
    private var registeredEndpoint = ""

    private val uiHandler = Handler(Looper.getMainLooper())
    private val refreshUi = object : Runnable {
        override fun run() {
            updateUI()
            if (UsqueVpnService.isRunning) {
                uiHandler.postDelayed(this, 500)
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        prefs = getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

        statusText = findViewById(R.id.status_text)
        connectButton = findViewById(R.id.connect_button)
        ipInfoText = findViewById(R.id.ip_info_text)
        settingsButton = findViewById(R.id.settings_button)
        sniText = findViewById(R.id.sni_text)
        endpointText = findViewById(R.id.endpoint_text)

        loadSavedSettings()
        requestNotificationPermissionIfNeeded()

        connectButton.setOnClickListener {
            if (UsqueVpnService.isRunning) stopVpn() else startVpn()
        }
        settingsButton.setOnClickListener { showSettingsDialog() }
        updateUI()
    }

    override fun onResume() {
        super.onResume()
        uiHandler.removeCallbacks(refreshUi)
        uiHandler.post(refreshUi)
    }

    override fun onPause() {
        uiHandler.removeCallbacks(refreshUi)
        super.onPause()
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(
                arrayOf(Manifest.permission.POST_NOTIFICATIONS),
                NOTIFICATION_REQUEST_CODE
            )
        }
    }

    private fun loadSavedSettings() {
        val savedSni = prefs.getString(KEY_SNI, "www.visa.cn") ?: "www.visa.cn"
        Usqueandroid.setSNI(savedSni)

        val savedEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        Usqueandroid.setEndpoint(savedEndpoint)
    }

    private fun saveSettings(sni: String, endpoint: String) {
        prefs.edit()
            .putString(KEY_SNI, sni.trim())
            .putString(KEY_ENDPOINT, endpoint.trim())
            .apply()
    }

    private fun showSettingsDialog() {
        val dialogView = layoutInflater.inflate(R.layout.dialog_settings, null)
        val sniInput = dialogView.findViewById<EditText>(R.id.sni_input)
        val endpointInput = dialogView.findViewById<EditText>(R.id.endpoint_input)
        val configPath = "${filesDir.absolutePath}/config.json"

        sniInput.setText(prefs.getString(KEY_SNI, Usqueandroid.getSNI()))
        val currentEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        endpointInput.setText(
            if (currentEndpoint.isNotEmpty()) currentEndpoint
            else Usqueandroid.getDefaultEndpoint(configPath)
        )

        AlertDialog.Builder(this)
            .setTitle("Connection Settings")
            .setView(dialogView)
            .setPositiveButton("Save") { _, _ ->
                val sni = sniInput.text.toString().trim()
                val endpoint = endpointInput.text.toString().trim()
                saveSettings(sni, endpoint)
                Usqueandroid.setSNI(sni)
                Usqueandroid.setEndpoint(endpoint)
                Toast.makeText(this, "Settings saved", Toast.LENGTH_SHORT).show()
                updateUI()
            }
            .setNegativeButton("Cancel", null)
            .setNeutralButton("Reset") { _, _ ->
                saveSettings("www.visa.cn", "")
                Usqueandroid.resetConnectionOptions()
                Toast.makeText(this, "Settings reset to defaults", Toast.LENGTH_SHORT).show()
                updateUI()
            }
            .show()
    }

    private fun startVpn() {
        val intent = VpnService.prepare(this)
        if (intent != null) {
            startActivityForResult(intent, VPN_REQUEST_CODE)
        } else {
            onVpnPermissionGranted()
        }
    }

    private fun stopVpn() {
        UsqueVpnService.stop()
        // If the service instance was not retained, deliver the same request
        // through Android's service lifecycle as a fallback.
        startService(Intent(this, UsqueVpnService::class.java).apply {
            action = UsqueVpnService.ACTION_DISCONNECT
        })
        updateUI()
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != VPN_REQUEST_CODE) return
        if (resultCode == RESULT_OK) {
            onVpnPermissionGranted()
        } else {
            Toast.makeText(this, "VPN permission denied", Toast.LENGTH_SHORT).show()
        }
    }

    private fun onVpnPermissionGranted() {
        val intent = Intent(this, UsqueVpnService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            ContextCompat.startForegroundService(this, intent)
        } else {
            startService(intent)
        }
        uiHandler.removeCallbacks(refreshUi)
        uiHandler.post(refreshUi)
    }

    private fun updateUI() {
        val configPath = "${filesDir.absolutePath}/config.json"

        if (UsqueVpnService.isRunning) {
            if (UsqueVpnService.isConnected) {
                statusText.text = "Connected"
                statusText.setTextColor(getColor(android.R.color.holo_green_dark))
            } else {
                statusText.text = "Connecting…"
                statusText.setTextColor(getColor(android.R.color.holo_orange_dark))
            }
            connectButton.text = "Disconnect"
            settingsButton.isEnabled = false
        } else {
            statusText.text = "Disconnected"
            statusText.setTextColor(getColor(android.R.color.holo_red_dark))
            connectButton.text = "Connect"
            settingsButton.isEnabled = true
        }

        // The Go config package intentionally has a process-wide config value.
        // Avoid reloading it from the UI while the service is registering or
        // starting the tunnel.
        if (!UsqueVpnService.isRunning) {
            if (Usqueandroid.isRegistered(configPath)) {
                val ipv4 = Usqueandroid.getAssignedIPv4(configPath)
                val ipv6 = Usqueandroid.getAssignedIPv6(configPath)
                ipInfoText.text = "IPv4: $ipv4\nIPv6: $ipv6"
            } else {
                ipInfoText.text = "Not registered"
            }
        }

        val currentSni = prefs.getString(KEY_SNI, Usqueandroid.getSNI()) ?: "www.visa.cn"
        sniText.text = "SNI: $currentSni"

        val currentEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        if (!UsqueVpnService.isRunning && currentEndpoint.isEmpty()) {
            registeredEndpoint = Usqueandroid.getDefaultEndpoint(configPath)
        }
        val displayEndpoint = if (currentEndpoint.isNotEmpty()) currentEndpoint else registeredEndpoint
        endpointText.text = "Endpoint: $displayEndpoint"
    }
}
