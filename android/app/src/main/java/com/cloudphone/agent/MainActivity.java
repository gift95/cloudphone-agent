package com.cloudphone.agent;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.net.Uri;
import android.os.PowerManager;
import android.provider.Settings;
import android.graphics.Color;
import android.graphics.Typeface;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.CompoundButton;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.Spinner;
import android.widget.TextView;
import android.widget.Toast;

import java.util.HashMap;
import java.util.Map;

/**
 * 前端页面（复刻版）：Agent 参数配置 + Shizuku 授权 + 启动/停止控制。
 * 参数与 cloudphone-agent 的命令行参数一一对应。
 */
public class MainActivity extends Activity implements CompoundButton.OnCheckedChangeListener {

    private static final String HINT_SIGNALING =
            "信令服务器地址，例如：\n"
                    + "ws://192.168.50.100:8443/register_agent";
    private static final String HINT_DEVICE_ID =
            "设备 ID，留空自动生成，例如：device-vivo-02";
    private static final String HINT_EXTERNAL_ADDR =
            "NAT 外部地址（公网 IP），例如：203.0.113.10\n用于 NAT1To1IP，内网穿透时必填";
    private static final String HINT_WEBRTC_PORT = "50000";
    private static final String HINT_ICE_SERVERS =
            "ICE 服务器列表，逗号分隔，例如：\n"
                    + "stun:stun.l.google.com:19302,turn:user:pass@turn.example.com:3478";
    private static final String HINT_RESOLUTION = "1080x2400（可选）";
    private static final String HINT_MAX_SIZE = "1920（可选，0=不限制）";
    private static final String HINT_MAX_FPS = "60（可选，0=不限制）";
    private static final String HINT_BITRATE = "2000000（目标码率 bps）";
    private static final String HINT_MAX_BITRATE = "0（最大码率 bps，0=不限制）";
    private static final String HINT_VIDEO_CODEC_OPTS =
            "视频编码器选项，例如：\n"
                    + "profile=1,level=4\n留空使用编码器默认参数";
    private static final String HINT_SNAPSHOT_INTERVAL = "0（截图间隔秒数，0=关闭）";
    private static final String HINT_TIMEZONE =
            "日志时区，例如：\n"
                    + "Asia/Shanghai, UTC, America/New_York";

    // 时区下拉选项（显示名称 -> 实际值）
    private static final String[][] TIMEZONE_OPTIONS = {
        {"Asia/Shanghai (中国标准时间 UTC+8)", "Asia/Shanghai"},
        {"Asia/Tokyo (日本标准时间 UTC+9)", "Asia/Tokyo"},
        {"Asia/Seoul (韩国标准时间 UTC+9)", "Asia/Seoul"},
        {"Asia/Singapore (新加坡时间 UTC+8)", "Asia/Singapore"},
        {"Asia/Hong_Kong (香港时间 UTC+8)", "Asia/Hong_Kong"},
        {"Asia/Taipei (台北时间 UTC+8)", "Asia/Taipei"},
        {"Asia/Bangkok (曼谷时间 UTC+7)", "Asia/Bangkok"},
        {"Asia/Dubai (迪拜时间 UTC+4)", "Asia/Dubai"},
        {"Asia/Kolkata (印度标准时间 UTC+5:30)", "Asia/Kolkata"},
        {"Europe/London (英国时间 UTC+0)", "Europe/London"},
        {"Europe/Paris (法国时间 UTC+1)", "Europe/Paris"},
        {"Europe/Berlin (德国时间 UTC+1)", "Europe/Berlin"},
        {"Europe/Moscow (莫斯科时间 UTC+3)", "Europe/Moscow"},
        {"America/New_York (美国东部时间 UTC-5)", "America/New_York"},
        {"America/Chicago (美国中部时间 UTC-6)", "America/Chicago"},
        {"America/Denver (美国山区时间 UTC-7)", "America/Denver"},
        {"America/Los_Angeles (美国太平洋时间 UTC-8)", "America/Los_Angeles"},
        {"America/Toronto (加拿大多伦多时间 UTC-5)", "America/Toronto"},
        {"America/Sao_Paulo (巴西圣保罗时间 UTC-3)", "America/Sao_Paulo"},
        {"Australia/Sydney (澳大利亚悉尼时间 UTC+10)", "Australia/Sydney"},
        {"Australia/Melbourne (澳大利亚墨尔本时间 UTC+10)", "Australia/Melbourne"},
        {"Pacific/Auckland (新西兰奥克兰时间 UTC+12)", "Pacific/Auckland"},
        {"UTC (协调世界时)", "UTC"},
    };

    private static final int LOG_REFRESH_INTERVAL_MS = 2000;

    private final Handler handler = new Handler(Looper.getMainLooper());
    private AgentConfig cfg;
    private LinearLayout container;
    private boolean logAutoRefresh = true;
    private final Runnable logRefreshRunnable = new Runnable() {
        @Override
        public void run() {
            if (logAutoRefresh) {
                refreshLog();
                handler.postDelayed(this, LOG_REFRESH_INTERVAL_MS);
            }
        }
    };

    private TextView statusView;
    private TextView shizukuView;
    private TextView logView;
    private ScrollView logScrollView;
    private final Map<String, EditText> fields = new HashMap<>();
    private final Map<String, CheckBox> checks = new HashMap<>();
    private Button btnPerm;
    private Button btnStart;
    private Button btnStop;

    // 高级参数折叠
    private TextView advancedHeader;
    private LinearLayout advancedGroup;
    private boolean advancedExpanded = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        cfg = new AgentConfig(this);
        setContentView(R.layout.activity_main);
        container = findViewById(R.id.container);
        buildUi();
        refreshStatus();
    }

    @Override
    protected void onResume() {
        super.onResume();
        refreshStatus();
        refreshLog();
        startLogAutoRefresh();
    }

    @Override
    protected void onPause() {
        super.onPause();
        stopLogAutoRefresh();
    }

    private void startLogAutoRefresh() {
        logAutoRefresh = true;
        handler.removeCallbacks(logRefreshRunnable);
        handler.postDelayed(logRefreshRunnable, LOG_REFRESH_INTERVAL_MS);
    }

    private void stopLogAutoRefresh() {
        logAutoRefresh = false;
        handler.removeCallbacks(logRefreshRunnable);
    }

    // ---------------- UI 构建 ----------------

    private void buildUi() {
        // 标题
        TextView title = new TextView(this);
        title.setText("云手机 Agent");
        title.setTextSize(22);
        title.setTypeface(null, Typeface.BOLD);
        title.setTextColor(Color.rgb(17, 24, 39));
        container.addView(title, matchWrap());

        // 版本号显示（APK 版本与内置 agent 版本一致）
        TextView versionView = new TextView(this);
        try {
            String pkgVersion = getPackageManager().getPackageInfo(getPackageName(), 0).versionName;
            versionView.setText("版本: " + pkgVersion + " (内置 Agent v" + pkgVersion + ")");
        } catch (Exception e) {
            versionView.setText("版本: unknown");
        }
        versionView.setTextSize(12);
        versionView.setTextColor(Color.rgb(107, 114, 128));
        versionView.setPadding(0, dp(2), 0, dp(2));
        container.addView(versionView, matchWrap());

        statusView = new TextView(this);
        statusView.setTextSize(14);
        statusView.setTextColor(Color.rgb(75, 85, 99));
        container.addView(statusView, matchWrapMargin(4));

        shizukuView = new TextView(this);
        shizukuView.setTextSize(13);
        shizukuView.setTextColor(Color.rgb(107, 114, 128));
        container.addView(shizukuView, matchWrapMargin(2));

        // Shizuku 授权按钮
        btnPerm = new Button(this);
        btnPerm.setText("申请 Shizuku 权限");
        btnPerm.setOnClickListener(v -> {
            if (!ShizukuLauncher.available()) {
                toast("Shizuku 未运行，请先安装并激活 Shizuku");
                return;
            }
            ShizukuLauncher.requestPermission(this);
        });
        container.addView(btnPerm, matchWrapMargin(8));

        // 电池优化豁免按钮
        Button btnBattery = new Button(this);
        btnBattery.setText(getBatteryButtonText());
        btnBattery.setOnClickListener(v -> requestBatteryOptimization());
        container.addView(btnBattery, matchWrapMargin(4));

        // 电池优化提示
        TextView batteryHint = new TextView(this);
        batteryHint.setText("💡 建议忽略电池优化，防止 Agent 被系统杀死");
        batteryHint.setTextSize(12);
        batteryHint.setTextColor(Color.rgb(107, 114, 128));
        batteryHint.setPadding(0, dp(2), 0, dp(4));
        container.addView(batteryHint, matchWrap());

        // 分隔线
        container.addView(divider(), matchWrapMargin(12));

        // 连接配置
        TextView connHeader = new TextView(this);
        connHeader.setText("连接配置");
        connHeader.setTextSize(16);
        connHeader.setTypeface(null, Typeface.BOLD);
        connHeader.setTextColor(Color.rgb(17, 24, 39));
        container.addView(connHeader, matchWrapMargin(4));

        addField(AgentConfig.KEY_SIGNALING, "信令服务器地址", HINT_SIGNALING, AgentConfig.DEFAULT_SIGNALING);
        addField(AgentConfig.KEY_DEVICE_ID, "设备 ID", HINT_DEVICE_ID, "");

        // 网络配置
        TextView netHeader = new TextView(this);
        netHeader.setText("网络配置");
        netHeader.setTextSize(16);
        netHeader.setTypeface(null, Typeface.BOLD);
        netHeader.setTextColor(Color.rgb(17, 24, 39));
        netHeader.setPadding(0, dp(8), 0, dp(4));
        container.addView(netHeader, matchWrap());

        addField(AgentConfig.KEY_EXTERNAL_ADDR, "NAT 外部地址", HINT_EXTERNAL_ADDR, "");
        addField(AgentConfig.KEY_WEBRTC_PORT, "WebRTC UDP 端口", HINT_WEBRTC_PORT, AgentConfig.DEFAULT_WEBRTC_PORT);
        addField(AgentConfig.KEY_ICE_SERVERS, "ICE 服务器", HINT_ICE_SERVERS, "");

        // 视频配置（仅保留高级选项，其他使用默认值）
        TextView videoHeader = new TextView(this);
        videoHeader.setText("视频配置");
        videoHeader.setTextSize(16);
        videoHeader.setTypeface(null, Typeface.BOLD);
        videoHeader.setTextColor(Color.rgb(17, 24, 39));
        videoHeader.setPadding(0, dp(12), 0, dp(4));
        container.addView(videoHeader, matchWrap());

        addField(AgentConfig.KEY_VIDEO_CODEC_OPTS, "视频编码器选项", HINT_VIDEO_CODEC_OPTS, "");

        // 高级配置（折叠）
        advancedHeader = new TextView(this);
        advancedHeader.setText("▼ 高级配置");
        advancedHeader.setTextSize(15);
        advancedHeader.setTypeface(null, Typeface.BOLD);
        advancedHeader.setTextColor(Color.rgb(37, 99, 235));
        advancedHeader.setPadding(0, dp(12), 0, dp(4));
        advancedHeader.setOnClickListener(v -> toggleAdvanced());
        container.addView(advancedHeader, matchWrap());

        advancedGroup = new LinearLayout(this);
        advancedGroup.setOrientation(LinearLayout.VERTICAL);
        advancedGroup.setVisibility(View.GONE);
        container.addView(advancedGroup, matchWrap());

        addSpinnerTo(advancedGroup, AgentConfig.KEY_TIMEZONE, "日志时区", TIMEZONE_OPTIONS, AgentConfig.DEFAULT_TIMEZONE);

        // 布尔选项
        addCheckTo(advancedGroup, AgentConfig.KEY_AUDIO, "启用音频", AgentConfig.DEFAULT_AUDIO);
        addCheckTo(advancedGroup, AgentConfig.KEY_ROOT, "Root 模式运行", AgentConfig.DEFAULT_ROOT);
        addCheckTo(advancedGroup, AgentConfig.KEY_DEBUG, "调试日志", AgentConfig.DEFAULT_DEBUG);

        // 应用设置
        addCheck(AgentConfig.KEY_AUTOSTART, "开机自启", AgentConfig.DEFAULT_AUTOSTART);

        // 分隔线
        container.addView(divider(), matchWrapMargin(12));

        // 控制按钮
        LinearLayout btnRow = new LinearLayout(this);
        btnRow.setOrientation(LinearLayout.HORIZONTAL);
        btnRow.setGravity(Gravity.CENTER);

        btnStart = new Button(this);
        btnStart.setText("启动 Agent");
        btnStart.setOnClickListener(v -> startAgent());
        btnRow.addView(btnStart, btnLayout());

        btnStop = new Button(this);
        btnStop.setText("停止 Agent");
        btnStop.setOnClickListener(v -> stopAgent());
        btnRow.addView(btnStop, btnLayout());

        container.addView(btnRow, matchWrapMargin(8));

        // 分隔线
        container.addView(divider(), matchWrapMargin(12));

        // 日志区域
        TextView logHeader = new TextView(this);
        logHeader.setText("运行日志");
        logHeader.setTextSize(16);
        logHeader.setTypeface(null, Typeface.BOLD);
        logHeader.setTextColor(Color.rgb(17, 24, 39));
        container.addView(logHeader, matchWrapMargin(4));

        // 日志按钮行
        LinearLayout logBtnRow = new LinearLayout(this);
        logBtnRow.setOrientation(LinearLayout.HORIZONTAL);
        logBtnRow.setGravity(Gravity.CENTER);

        Button btnAutoRefresh = new Button(this);
        btnAutoRefresh.setText("自动刷新: 开");
        btnAutoRefresh.setOnClickListener(v -> {
            logAutoRefresh = !logAutoRefresh;
            btnAutoRefresh.setText(logAutoRefresh ? "自动刷新: 开" : "自动刷新: 关");
            if (logAutoRefresh) {
                startLogAutoRefresh();
            } else {
                stopLogAutoRefresh();
            }
        });
        logBtnRow.addView(btnAutoRefresh, btnLayout());

        Button btnRefreshLog = new Button(this);
        btnRefreshLog.setText("刷新");
        btnRefreshLog.setOnClickListener(v -> refreshLog());
        logBtnRow.addView(btnRefreshLog, btnLayout());

        Button btnClearLog = new Button(this);
        btnClearLog.setText("清空");
        btnClearLog.setOnClickListener(v -> {
            AgentBootstrap.clearLog();
            refreshLog();
            toast("日志已清空");
        });
        logBtnRow.addView(btnClearLog, btnLayout());

        Button btnCopyLog = new Button(this);
        btnCopyLog.setText("复制");
        btnCopyLog.setOnClickListener(v -> {
            ClipboardManager cm = (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
            cm.setPrimaryClip(ClipData.newPlainText("log", logView.getText()));
            toast("日志已复制到剪贴板");
        });
        logBtnRow.addView(btnCopyLog, btnLayout());

        container.addView(logBtnRow, matchWrapMargin(4));

        logScrollView = new ScrollView(this);
        logScrollView.setBackgroundColor(Color.rgb(17, 24, 39));
        logScrollView.setPadding(dp(8), dp(8), dp(8), dp(8));

        logView = new TextView(this);
        logView.setTextSize(11);
        logView.setTextColor(Color.rgb(209, 213, 219));
        logView.setTypeface(Typeface.MONOSPACE);
        logView.setLineSpacing(0, 1.2f);
        logScrollView.addView(logView, matchWrap());

        LinearLayout.LayoutParams logParams = matchWrapMargin(8);
        logParams.height = dp(200);
        container.addView(logScrollView, logParams);
    }

    private void toggleAdvanced() {
        advancedExpanded = !advancedExpanded;
        advancedGroup.setVisibility(advancedExpanded ? View.VISIBLE : View.GONE);
        advancedHeader.setText(advancedExpanded ? "▲ 高级配置" : "▼ 高级配置");
    }

    // ---------------- 字段辅助方法 ----------------

    private void addField(String key, String label, String hint, String defValue) {
        addFieldTo(container, key, label, hint, defValue);
    }

    private void addFieldTo(ViewGroup parent, String key, String label, String hint, String defValue) {
        TextView labelView = new TextView(this);
        labelView.setText(label);
        labelView.setTextSize(13);
        labelView.setTextColor(Color.rgb(55, 65, 81));
        labelView.setPadding(0, dp(8), 0, dp(2));
        parent.addView(labelView, matchWrap());

        EditText et = new EditText(this);
        et.setText(cfg.get(key, defValue));
        et.setHint(hint);
        et.setTextSize(14);
        et.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE);
        et.setSingleLine(false);
        et.setMinLines(1);
        et.setMaxLines(3);
        et.setBackgroundResource(R.drawable.bg_field);
        et.setPadding(dp(10), dp(8), dp(10), dp(8));
        et.setOnFocusChangeListener((v, hasFocus) -> {
            if (!hasFocus) {
                cfg.set(key, et.getText().toString().trim());
            }
        });
        parent.addView(et, matchWrapMargin(2));
        fields.put(key, et);
    }

    private void addCheck(String key, String label, boolean defValue) {
        addCheckTo(container, key, label, defValue);
    }

    private void addCheckTo(ViewGroup parent, String key, String label, boolean defValue) {
        CheckBox cb = new CheckBox(this);
        cb.setText(label);
        cb.setTextSize(14);
        cb.setTextColor(Color.rgb(31, 41, 55));
        cb.setChecked(cfg.getBool(key, defValue));
        cb.setOnCheckedChangeListener(this);
        cb.setTag(key);
        cb.setPadding(0, dp(6), 0, dp(6));
        parent.addView(cb, matchWrap());
        checks.put(key, cb);
    }

    /**
     * 添加下拉选择框。
     * @param options 二维数组，每项为 [显示名称, 实际值]
     */
    private void addSpinnerTo(ViewGroup parent, String key, String label, String[][] options, String defValue) {
        TextView labelView = new TextView(this);
        labelView.setText(label);
        labelView.setTextSize(13);
        labelView.setTextColor(Color.rgb(55, 65, 81));
        labelView.setPadding(0, dp(8), 0, dp(2));
        parent.addView(labelView, matchWrap());

        // 构建显示名称数组
        String[] displayNames = new String[options.length];
        for (int i = 0; i < options.length; i++) {
            displayNames[i] = options[i][0];
        }

        Spinner spinner = new Spinner(this);
        ArrayAdapter<String> adapter = new ArrayAdapter<>(
                this, android.R.layout.simple_spinner_item, displayNames);
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        spinner.setAdapter(adapter);
        spinner.setBackgroundResource(R.drawable.bg_field);
        spinner.setPadding(dp(10), dp(8), dp(10), dp(8));

        // 设置当前值
        String currentValue = cfg.get(key, defValue);
        int selectedIndex = 0;
        for (int i = 0; i < options.length; i++) {
            if (options[i][1].equals(currentValue)) {
                selectedIndex = i;
                break;
            }
        }
        spinner.setSelection(selectedIndex);

        // 选择变化时保存
        final String finalKey = key;
        spinner.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override
            public void onItemSelected(AdapterView<?> parent, View view, int position, long id) {
                cfg.set(finalKey, options[position][1]);
            }

            @Override
            public void onNothingSelected(AdapterView<?> parent) {
            }
        });

        parent.addView(spinner, matchWrapMargin(2));
    }

    @Override
    public void onCheckedChanged(CompoundButton buttonView, boolean isChecked) {
        String key = (String) buttonView.getTag();
        if (key != null) {
            cfg.set(key, isChecked);
        }
    }

    // ---------------- 布局辅助方法 ----------------

    private View divider() {
        View v = new View(this);
        v.setBackgroundColor(Color.rgb(229, 231, 235));
        v.setLayoutParams(new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(1)));
        return v;
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT);
    }

    private LinearLayout.LayoutParams matchWrapMargin(int marginDp) {
        LinearLayout.LayoutParams p = matchWrap();
        p.setMargins(0, dp(marginDp), 0, 0);
        return p;
    }

    private LinearLayout.LayoutParams btnLayout() {
        LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(
                0, ViewGroup.LayoutParams.WRAP_CONTENT, 1);
        p.setMargins(dp(4), 0, dp(4), 0);
        return p;
    }

    private int dp(int dp) {
        return (int) (dp * getResources().getDisplayMetrics().density + 0.5f);
    }

    // ---------------- Agent 控制 ----------------

    private void startAgent() {
        // 保存所有字段
        for (Map.Entry<String, EditText> e : fields.entrySet()) {
            cfg.set(e.getKey(), e.getValue().getText().toString().trim());
        }

        if (!ShizukuLauncher.available()) {
            toast("Shizuku 未运行，请先安装并激活 Shizuku");
            return;
        }
        if (!ShizukuLauncher.granted()) {
            toast("未授权 Shizuku 权限，请先点击「申请 Shizuku 权限」");
            return;
        }

        btnStart.setEnabled(false);
        btnStart.setText("启动中...");

        new Thread(() -> {
            try {
                boolean ok = AgentBootstrap.start(this, cfg);
                handler.post(() -> {
                    btnStart.setEnabled(true);
                    btnStart.setText("启动 Agent");
                    toast(ok ? "Agent 启动命令已发送" : "启动失败，请查看日志");
                    refreshStatus();
                    refreshLog();
                });
            } catch (Exception e) {
                handler.post(() -> {
                    btnStart.setEnabled(true);
                    btnStart.setText("启动 Agent");
                    toast("启动异常: " + e.getMessage());
                });
            }
        }).start();
    }

    private void stopAgent() {
        if (!ShizukuLauncher.available() || !ShizukuLauncher.granted()) {
            toast("Shizuku 未授权，无法停止");
            return;
        }

        new Thread(() -> {
            try {
                AgentBootstrap.stop();
                handler.post(() -> {
                    toast("Agent 停止命令已发送");
                    refreshStatus();
                });
            } catch (Exception e) {
                handler.post(() -> toast("停止异常: " + e.getMessage()));
            }
        }).start();
    }

    // ---------------- 状态刷新 ----------------

    private void refreshStatus() {
        boolean shizukuAvail = ShizukuLauncher.available();
        boolean shizukuGranted = ShizukuLauncher.granted();

        // 更新电池优化按钮状态
        updateBatteryButton();

        if (shizukuAvail && shizukuGranted) {
            shizukuView.setText("✓ Shizuku 已授权");
            shizukuView.setTextColor(Color.rgb(22, 163, 74));
            btnPerm.setVisibility(View.GONE);
        } else if (shizukuAvail) {
            shizukuView.setText("⚠ Shizuku 已运行，但未授权本应用");
            shizukuView.setTextColor(Color.rgb(234, 88, 12));
            btnPerm.setVisibility(View.VISIBLE);
        } else {
            shizukuView.setText("✗ Shizuku 未运行，请先安装并激活 Shizuku");
            shizukuView.setTextColor(Color.rgb(220, 38, 38));
            btnPerm.setVisibility(View.VISIBLE);
        }

        new Thread(() -> {
            boolean running = AgentBootstrap.isRunning();
            handler.post(() -> {
                if (running) {
                    statusView.setText("● Agent 运行中");
                    statusView.setTextColor(Color.rgb(22, 163, 74));
                } else {
                    statusView.setText("○ Agent 未运行");
                    statusView.setTextColor(Color.rgb(107, 114, 128));
                }
            });
        }).start();
    }

    private void refreshLog() {
        new Thread(() -> {
            String log = AgentBootstrap.logTail();
            handler.post(() -> {
                logView.setText(log);
                logScrollView.post(() -> logScrollView.fullScroll(View.FOCUS_DOWN));
            });
        }).start();
    }

    // ---------------- 权限结果回调 ----------------

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions, int[] grantResults) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults);
        if (requestCode == ShizukuLauncher.REQ_PERMISSION) {
            refreshStatus();
            if (ShizukuLauncher.granted()) {
                toast("Shizuku 授权成功");
            } else {
                toast("Shizuku 授权被拒绝");
            }
        }
    }

    // ---------------- 电池优化豁免 ----------------

    private boolean isBatteryOptimizationIgnored() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            return pm != null && pm.isIgnoringBatteryOptimizations(getPackageName());
        }
        return true; // Android 6.0 以下不需要
    }

    private String getBatteryButtonText() {
        return isBatteryOptimizationIgnored() ? "✓ 已忽略电池优化" : "申请忽略电池优化";
    }

    private void updateBatteryButton() {
        // 找到电池优化按钮并更新文字（通过遍历 container 查找）
        // 简单方式：重新设置按钮文字（按钮在 container 中第 4 个位置左右）
        for (int i = 0; i < container.getChildCount(); i++) {
            View child = container.getChildAt(i);
            if (child instanceof Button) {
                Button btn = (Button) child;
                String text = btn.getText().toString();
                if (text.contains("电池优化") || text.contains("已忽略")) {
                    btn.setText(getBatteryButtonText());
                    break;
                }
            }
        }
    }

    private void requestBatteryOptimization() {
        if (isBatteryOptimizationIgnored()) {
            toast("已忽略电池优化，无需重复申请");
            return;
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            try {
                Intent intent = new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
                intent.setData(Uri.parse("package:" + getPackageName()));
                startActivity(intent);
                toast("请在弹出的对话框中选择\"允许\"");
            } catch (Exception e) {
                // 某些设备可能不支持直接跳转，打开电池优化设置页面
                Intent intent = new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS);
                startActivity(intent);
                toast("请在列表中找到本应用并选择\"不限制\"");
            }
        } else {
            toast("当前 Android 版本不需要电池优化设置");
        }
    }

    private void toast(String msg) {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show();
    }
}
