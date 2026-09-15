package com.cloudphone.agent;

import android.content.Context;
import android.content.SharedPreferences;

/**
 * Agent 配置模型（复刻版）：与 cloudphone-agent 的命令行参数一一对应，
 * 持久化到 SharedPreferences，运行时翻译为命令行参数。
 */
public class AgentConfig {

    // 必选参数
    public static final String KEY_SIGNALING = "signaling";
    public static final String KEY_DEVICE_ID = "device_id";

    // 网络配置
    public static final String KEY_EXTERNAL_ADDR = "external_addr";
    public static final String KEY_WEBRTC_PORT = "webrtc_port";
    public static final String KEY_ICE_SERVERS = "ice_servers";

    // 视频配置
    public static final String KEY_RESOLUTION = "resolution";
    public static final String KEY_MAX_SIZE = "max_size";
    public static final String KEY_MAX_FPS = "max_fps";
    public static final String KEY_BITRATE = "bitrate";
    public static final String KEY_MAX_BITRATE = "max_bitrate";
    public static final String KEY_VIDEO_CODEC_OPTS = "video_codec_opts";
    public static final String KEY_SNAPSHOT_INTERVAL = "snapshot_interval";

    // 布尔参数
    public static final String KEY_AUDIO = "audio";
    public static final String KEY_ROOT = "root";
    public static final String KEY_DEBUG = "debug";

    // 其他配置
    public static final String KEY_TIMEZONE = "timezone";

    // 应用设置
    public static final String KEY_AUTOSTART = "autostart";

    // 默认值
    public static final String DEFAULT_SIGNALING = "ws://192.168.50.100:8443/register_agent";
    public static final String DEFAULT_WEBRTC_PORT = "50000";
    public static final String DEFAULT_BITRATE = "2000000";
    public static final String DEFAULT_TIMEZONE = "Asia/Shanghai";
    public static final boolean DEFAULT_AUDIO = true;
    public static final boolean DEFAULT_ROOT = false;
    public static final boolean DEFAULT_DEBUG = false;
    public static final boolean DEFAULT_AUTOSTART = true;

    private final SharedPreferences sp;

    public AgentConfig(Context ctx) {
        sp = ctx.getSharedPreferences("agent_cfg", Context.MODE_PRIVATE);
    }

    public String get(String key, String def) {
        return sp.getString(key, def);
    }

    public boolean getBool(String key, boolean def) {
        return sp.getBoolean(key, def);
    }

    public void set(String key, String v) {
        sp.edit().putString(key, v).apply();
    }

    public void set(String key, boolean v) {
        sp.edit().putBoolean(key, v).apply();
    }

    /** 重置为默认值。 */
    public void reset() {
        sp.edit().clear().apply();
    }
}
