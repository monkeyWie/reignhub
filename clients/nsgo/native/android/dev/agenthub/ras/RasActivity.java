package dev.agenthub.ras;

import android.os.Bundle;
import android.os.Build;
import android.content.Intent;
import android.provider.Settings;
import android.view.View;
import android.view.ViewGroup;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.view.WindowManager;
import dev.oneo.nsgo.NsgoActivity;
import dev.oneo.nsgo.NsgoView;
import java.util.HashMap;

/** The official NSGo host API keeps native features outside app/protocol code. */
public final class RasActivity extends NsgoActivity {
    boolean resumed;
    boolean screenAwake;
    NsgoView rasView;
    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        // Android also chooses an IME state when a newly opened window gains focus.
        getWindow().setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_HIDDEN
                | WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE);
        NsgoView view = (NsgoView)((ViewGroup)findViewById(android.R.id.content)).getChildAt(0);
        rasView = view;
        view.handle("ras.focus-composer", (data, reply) -> {
            View entry = data instanceof String ? view.findViewWithTag(data) : null;
            View field = entry == null ? null : entry.findViewWithTag("chat-input");
            if (field instanceof EditText && field.isShown() && field.isEnabled() && !field.hasFocus()) {
                EditText editor = (EditText) field;
                boolean showOnFocus = editor.getShowSoftInputOnFocus();
                editor.setShowSoftInputOnFocus(false);
                try {
                    if (editor.requestFocus()) {
                        // A keyboard from the previous conversation may still be visible.
                        getSystemService(InputMethodManager.class).hideSoftInputFromWindow(editor.getWindowToken(), 0);
                    }
                } finally {
                    editor.setShowSoftInputOnFocus(showOnFocus);
                }
            }
            reply.resolve(null);
        });
        view.handle("ras.screen-awake", (data, reply) -> {
            screenAwake = data instanceof java.util.Map && Boolean.TRUE.equals(((java.util.Map<?,?>)data).get("enabled"));
            updateScreenAwake();
            reply.resolve(null);
        });
        ConnectionService.listener = running -> {
            HashMap<String,Object> stateInfo = new HashMap<>();
            stateInfo.put("running", running);
            view.send("ras.background-state", stateInfo);
        };
        ConnectionService.networkListener = () -> view.send("ras.network-change", new HashMap<String,Object>());
        view.handle("ras.background", (data, reply) -> ConnectionService.request(this, resumed, data, reply));
        view.handle("ras.background-settings", (data, reply) -> {
            // Open the system list; changing battery exemptions stays the user's choice.
            startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
            reply.resolve(null);
        });
        view.handle("ras.timezone", (data, reply) -> {
            java.util.TimeZone zone = java.util.TimeZone.getDefault();
            HashMap<String,Object> result = new HashMap<>();
            result.put("name", zone.getID());
            result.put("offsetSeconds", zone.getOffset(System.currentTimeMillis()) / 1000);
            reply.resolve(result);
        });
        view.handle("ras.device", (data, reply) -> {
            String name = null;
            try { name = Settings.Global.getString(getContentResolver(), "device_name"); }
            catch (SecurityException ignored) { /* Some OEMs protect this setting. */ }
            String model = Build.MODEL == null ? "" : Build.MODEL.trim();
            String brand = Build.MANUFACTURER == null ? "" : Build.MANUFACTURER.trim();
            // Keep a user/OEM friendly name. Add the brand only for a bare model code.
            if (name == null || name.trim().isEmpty()) name = model;
            name = name.trim();
            if (name.equalsIgnoreCase(model) && model.matches("(?i)(?=.*\\d)[a-z0-9_-]+") && !brand.isEmpty()
                    && !name.toLowerCase(java.util.Locale.ROOT).startsWith(brand.toLowerCase(java.util.Locale.ROOT)))
                name = brand + " " + name;
            if (name.isEmpty()) name = "Android";
            HashMap<String,Object> result = new HashMap<>();
            result.put("name", name);
            result.put("model", model);
            result.put("platform", "android");
            reply.resolve(result);
        });
        openSession(getIntent());
    }
    @Override protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        openSession(intent);
    }
    private void openSession(Intent intent) {
        String encoded = intent.getStringExtra(ConnectionService.SESSION);
        if (encoded == null || rasView == null) return;
        try {
            org.json.JSONObject task = new org.json.JSONObject(encoded);
            HashMap<String,Object> session = new HashMap<>();
            for (String key : new String[]{"target", "providerId", "id", "title"})
                session.put(key, task.optString(key));
            rasView.send("ras.open-session", session);
        } catch (org.json.JSONException ignored) { /* Ignore malformed external intents. */ }
        intent.removeExtra(ConnectionService.SESSION);
    }
    @Override protected void onResume() {
        super.onResume(); resumed = true;
        updateScreenAwake();
    }
    @Override protected void onPause() {
        resumed = false;
        updateScreenAwake();
        super.onPause();
    }
    @Override protected void onDestroy() {
        screenAwake = false;
        updateScreenAwake();
        ConnectionService.listener = null;
        ConnectionService.networkListener = null;
        stopService(new Intent(this, ConnectionService.class));
        super.onDestroy();
    }
    private void updateScreenAwake() {
        if (screenAwake && resumed) getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        else getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
    }
}
