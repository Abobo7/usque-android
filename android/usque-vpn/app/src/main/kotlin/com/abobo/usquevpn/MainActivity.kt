package com.abobo.usquevpn

import android.app.Activity
import android.app.AlertDialog
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.net.VpnService
import android.os.Bundle
import android.widget.*
import usqueandroid.Usqueandroid

class MainActivity : Activity() {

    companion object {
        private const val VPN_REQUEST_CODE = 1001
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

        // Load saved settings into Go library
        loadSavedSettings()

        connectButton.setOnClickListener {
            if (UsqueVpnService.isRunning) {
                stopVpn()
            } else {
                startVpn()
            }
        }

        settingsButton.setOnClickListener {
            showSettingsDialog()
        }

        updateUI()
    }

    override fun onResume() {
        super.onResume()
        updateUI()
    }

    private fun loadSavedSettings() {
        // Load SNI
        val savedSni = prefs.getString(KEY_SNI, "www.visa.cn") ?: "www.visa.cn"
        Usqueandroid.setSNI(savedSni)
        
        // Load endpoint
        val savedEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        if (savedEndpoint.isNotEmpty()) {
            Usqueandroid.setEndpoint(savedEndpoint)
        }
    }

    private fun saveSettings(sni: String, endpoint: String) {
        prefs.edit()
            .putString(KEY_SNI, sni)
            .putString(KEY_ENDPOINT, endpoint)
            .apply()
    }

    private fun showSettingsDialog() {
        val dialogView = layoutInflater.inflate(R.layout.dialog_settings, null)
        
        val sniInput = dialogView.findViewById<EditText>(R.id.sni_input)
        val endpointInput = dialogView.findViewById<EditText>(R.id.endpoint_input)
        
        val configPath = "${filesDir.absolutePath}/config.json"
        
        // Load current values from prefs (persisted) 
        sniInput.setText(prefs.getString(KEY_SNI, Usqueandroid.getSNI()))
        
        val currentEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        if (currentEndpoint.isNotEmpty()) {
            endpointInput.setText(currentEndpoint)
        } else {
            endpointInput.setText(Usqueandroid.getDefaultEndpoint(configPath))
        }
        
        AlertDialog.Builder(this)
            .setTitle("Connection Settings")
            .setView(dialogView)
            .setPositiveButton("Save") { _, _ ->
                val sni = sniInput.text.toString()
                val endpoint = endpointInput.text.toString()
                
                // Save to SharedPreferences
                saveSettings(sni, endpoint)
                
                // Apply to Go library
                Usqueandroid.setSNI(sni)
                Usqueandroid.setEndpoint(endpoint)
                
                Toast.makeText(this, "Settings saved", Toast.LENGTH_SHORT).show()
                updateUI()
            }
            .setNegativeButton("Cancel", null)
            .setNeutralButton("Reset") { _, _ ->
                // Reset to defaults
                saveSettings("www.visa.cn", "")
                Usqueandroid.resetConnectionOptions()
                Toast.makeText(this, "Settings reset to defaults", Toast.LENGTH_SHORT).show()
                updateUI()
            }
            .show()
    }

    private fun startVpn() {
        // Request VPN permission from system
        val intent = VpnService.prepare(this)
        if (intent != null) {
            startActivityForResult(intent, VPN_REQUEST_CODE)
        } else {
            // Permission already granted
            onVpnPermissionGranted()
        }
    }

    private fun stopVpn() {
        // Method 1: Use static stop (direct)
        UsqueVpnService.stop()
        
        // Method 2: Send disconnect intent (backup)
        val intent = Intent(this, UsqueVpnService::class.java)
        intent.action = UsqueVpnService.ACTION_DISCONNECT
        startService(intent)
        
        // Update UI after a delay to ensure service has stopped
        connectButton.postDelayed({
            updateUI()
        }, 1000)
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == VPN_REQUEST_CODE) {
            if (resultCode == RESULT_OK) {
                onVpnPermissionGranted()
            } else {
                Toast.makeText(this, "VPN permission denied", Toast.LENGTH_SHORT).show()
            }
        }
    }

    private fun onVpnPermissionGranted() {
        val intent = Intent(this, UsqueVpnService::class.java)
        startService(intent)
        
        // Update UI after a short delay to allow service to start
        connectButton.postDelayed({
            updateUI()
        }, 1500)
    }

    private fun updateUI() {
        val configPath = "${filesDir.absolutePath}/config.json"
        
        if (UsqueVpnService.isRunning) {
            statusText.text = "Connected"
            statusText.setTextColor(getColor(android.R.color.holo_green_dark))
            connectButton.text = "Disconnect"
            settingsButton.isEnabled = false
        } else {
            statusText.text = "Disconnected"
            statusText.setTextColor(getColor(android.R.color.holo_red_dark))
            connectButton.text = "Connect"
            settingsButton.isEnabled = true
        }

        // Show assigned IP if registered
        if (Usqueandroid.isRegistered(configPath)) {
            val ipv4 = Usqueandroid.getAssignedIPv4(configPath)
            val ipv6 = Usqueandroid.getAssignedIPv6(configPath)
            ipInfoText.text = "IPv4: $ipv4\nIPv6: $ipv6"
        } else {
            ipInfoText.text = "Not registered"
        }
        
        // Show current settings (from prefs for persistence)
        val currentSni = prefs.getString(KEY_SNI, Usqueandroid.getSNI()) ?: "www.visa.cn"
        sniText.text = "SNI: $currentSni"
        
        val currentEndpoint = prefs.getString(KEY_ENDPOINT, "") ?: ""
        val displayEndpoint = if (currentEndpoint.isNotEmpty()) {
            currentEndpoint
        } else {
            Usqueandroid.getDefaultEndpoint(configPath)
        }
        endpointText.text = "Endpoint: $displayEndpoint"
    }
}
