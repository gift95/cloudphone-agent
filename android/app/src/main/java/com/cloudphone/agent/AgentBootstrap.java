package com.cloudphone.agent;

import android.content.Context;
import android.os.Build;
import android.util.Log;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;

/**
 * Agent 引导器（复刻版适配）：
 * 1. 检测设备 CPU 架构，从 assets 中释放对应架构的 cloudphone-agent 与 libsys_core.so
 *    到外部存储专属目录（应用专属外部目录：应用无需权限即可写；shell/Shizuku 通过 FUSE 可读，
 *    应用私有目录 shell 读不了）；
 * 2. 支持 arm64-v8a、armeabi-v7a、x86_64 三种架构；
 * 3. 依据 AgentConfig 生成启动脚本（使用命令行参数，而非环境变量）；
 * 4. 通过 Shizuku 以 shell 权限将二进制复制到 /data/local/tmp 并后台拉起；
 * 5. 提供停止与状态查询。
 */
public final class AgentBootstrap {

    private static final String TAG = "AgentBootstrap";
    private static final String AGENT_NAME = "cloudphone-agent";
    private static final String LIB_NAME = "libsys_core.so";
    private static final String LOG_FILE = "/data/local/tmp/self-agent.log";

    private AgentBootstrap() {
    }

    /**
     * 检测设备 CPU 架构，返回对应的 agent asset 路径。
     * 支持：arm64-v8a、armeabi-v7a、x86_64
     */
    public static String pickAgentAsset() {
        String arch = getDeviceArch();
        return "agent/" + arch + "/" + AGENT_NAME;
    }

    /**
     * 获取设备主架构，映射到 agent 构建的架构目录名。
     * @return "arm64" / "armeabi-v7a" / "amd64"，默认返回 "arm64"
     */
    public static String getDeviceArch() {
        if (Build.SUPPORTED_ABIS != null && Build.SUPPORTED_ABIS.length > 0) {
            String abi = Build.SUPPORTED_ABIS[0];
            if ("arm64-v8a".equals(abi)) return "arm64";
            if ("armeabi-v7a".equals(abi) || "armeabi".equals(abi)) return "armeabi-v7a";
            if ("x86_64".equals(abi)) return "amd64";
            if ("x86".equals(abi)) return "amd64"; // x86 设备用 amd64 兼容（大多数 x86 设备支持 64 位）
            Log.w(TAG, "Unknown ABI: " + abi + ", fallback to arm64");
        }
        return "arm64"; // 默认 arm64
    }

    /** 应用专属外部目录（shell 可读）。 */
    public static File externalRoot(Context ctx) {
        File ext = ctx.getExternalFilesDir(null);
        return ext != null ? ext : ctx.getFilesDir();
    }

    /** 释放 assets 中的二进制到外部存储专属目录。 */
    public static synchronized boolean extract(Context ctx) throws IOException {
        File root = externalRoot(ctx);
        File binDir = new File(root, "bin");
        File libDir = new File(root, "lib");
        if (!binDir.exists()) binDir.mkdirs();
        if (!libDir.exists()) libDir.mkdirs();

        String asset = pickAgentAsset();
        boolean copied = copyAsset(ctx, asset, new File(binDir, AGENT_NAME));
        copied = copyAsset(ctx, LIB_NAME, new File(libDir, LIB_NAME)) && copied;
        return copied;
    }

    private static boolean copyAsset(Context ctx, String asset, File dst) throws IOException {
        // 每次都强制重新释放，确保升级 APK 后 agent 二进制同步更新
        try (InputStream in = ctx.getAssets().open(asset);
             FileOutputStream out = new FileOutputStream(dst)) {
            byte[] buf = new byte[65536];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
        }
        Log.d(TAG, "extracted " + asset + " -> " + dst + " (" + dst.length() + " bytes)");
        return true;
    }

    private static String q(String v) {
        return "'" + v.replace("'", "'\\''") + "'";
    }

    /**
     * 生成启动脚本（复刻版：使用命令行参数）。
     *
     * 等价于：
     * cd /data/local/tmp && CLASSPATH=/data/local/tmp/libsys_core.so nohup /data/local/tmp/cloudphone-agent-arm64 \
     *   -signaling ws://... \
     *   -id device-xxx \
     *   -jar /data/local/tmp/libsys_core.so \
     *   -bitrate 2000000 \
     *   -audio=true \
     *   -debug \
     *   >/data/local/tmp/self-agent.log 2>&1 &
     */
    public static String buildStartScript(Context ctx, AgentConfig cfg, File binFile, File libFile) {
        StringBuilder sb = new StringBuilder();
        sb.append("#!/system/bin/sh\n");

        // 先杀掉旧进程
        sb.append("pkill -f cloudphone-agent 2>/dev/null; pkill -f com.genymobile.scrcpy 2>/dev/null; true\n");

        // 复制二进制到 /data/local/tmp
        sb.append("cp ").append(q(binFile.getAbsolutePath())).append(" /data/local/tmp/cloudphone-agent-arm64\n");
        sb.append("chmod 755 /data/local/tmp/cloudphone-agent-arm64\n");
        sb.append("cp ").append(q(libFile.getAbsolutePath())).append(" /data/local/tmp/").append(LIB_NAME).append("\n");
        sb.append("chmod 644 /data/local/tmp/").append(LIB_NAME).append("\n");

        // 构建命令行参数
        sb.append("cd /data/local/tmp\n");
        sb.append("CLASSPATH=/data/local/tmp/").append(LIB_NAME).append(" setsid nohup /data/local/tmp/cloudphone-agent-arm64 \\\n");

        // 必选参数
        String signaling = cfg.get(AgentConfig.KEY_SIGNALING, "ws://192.168.50.100:8443/register_agent");
        sb.append("  -signaling ").append(q(signaling)).append(" \\\n");

        String deviceId = cfg.get(AgentConfig.KEY_DEVICE_ID, "");
        if (deviceId == null || deviceId.trim().isEmpty()) {
            // 自动生成设备 ID：device-<brand>-<model>
            deviceId = "device-android-" + Build.MODEL.replaceAll("[^a-zA-Z0-9]", "").toLowerCase();
        }
        sb.append("  -id ").append(q(deviceId)).append(" \\\n");

        sb.append("  -jar ").append(q("/data/local/tmp/" + LIB_NAME)).append(" \\\n");

        // 网络配置
        String externalAddr = cfg.get(AgentConfig.KEY_EXTERNAL_ADDR, "");
        if (externalAddr != null && !externalAddr.trim().isEmpty()) {
            sb.append("  -external-addr ").append(q(externalAddr)).append(" \\n");
        }

        String webrtcPort = cfg.get(AgentConfig.KEY_WEBRTC_PORT, "50000");
        if (webrtcPort != null && !webrtcPort.trim().isEmpty()) {
            sb.append("  -webrtc-port ").append(q(webrtcPort)).append(" \\n");
        }

        String iceServers = cfg.get(AgentConfig.KEY_ICE_SERVERS, "");
        if (iceServers != null && !iceServers.trim().isEmpty()) {
            sb.append("  -ice-servers ").append(q(iceServers)).append(" \\n");
        }

        // 视频配置
        String resolution = cfg.get(AgentConfig.KEY_RESOLUTION, "");
        if (resolution != null && !resolution.trim().isEmpty()) {
            sb.append("  -resolution ").append(q(resolution)).append(" \\n");
        }

        String maxSize = cfg.get(AgentConfig.KEY_MAX_SIZE, "");
        if (maxSize != null && !maxSize.trim().isEmpty()) {
            sb.append("  -max-size ").append(q(maxSize)).append(" \\n");
        }

        String maxFps = cfg.get(AgentConfig.KEY_MAX_FPS, "");
        if (maxFps != null && !maxFps.trim().isEmpty()) {
            sb.append("  -max-fps ").append(q(maxFps)).append(" \\n");
        }

        String bitrate = cfg.get(AgentConfig.KEY_BITRATE, "2000000");
        if (bitrate != null && !bitrate.trim().isEmpty()) {
            sb.append("  -bitrate ").append(q(bitrate)).append(" \\n");
        }

        String maxBitrate = cfg.get(AgentConfig.KEY_MAX_BITRATE, "");
        if (maxBitrate != null && !maxBitrate.trim().isEmpty()) {
            sb.append("  -max-bitrate ").append(q(maxBitrate)).append(" \\n");
        }

        String videoCodecOpts = cfg.get(AgentConfig.KEY_VIDEO_CODEC_OPTS, "");
        if (videoCodecOpts != null && !videoCodecOpts.trim().isEmpty()) {
            sb.append("  -video-codec-options ").append(q(videoCodecOpts)).append(" \\n");
        }

        String snapshotInterval = cfg.get(AgentConfig.KEY_SNAPSHOT_INTERVAL, "");
        if (snapshotInterval != null && !snapshotInterval.trim().isEmpty()) {
            sb.append("  -snapshot-interval ").append(q(snapshotInterval)).append(" \\n");
        }

        // 其他配置
        String timezone = cfg.get(AgentConfig.KEY_TIMEZONE, "");
        if (timezone != null && !timezone.trim().isEmpty()) {
            sb.append("  -timezone ").append(q(timezone)).append(" \\n");
        }

        // 布尔参数
        if (cfg.getBool(AgentConfig.KEY_AUDIO, true)) {
            sb.append("  -audio=true \\n");
        }

        if (cfg.getBool(AgentConfig.KEY_ROOT, false)) {
            sb.append("  -root \\n");
        }

        if (cfg.getBool(AgentConfig.KEY_DEBUG, false)) {
            sb.append("  -debug \\n");
        }

        // 日志重定向
        sb.append("  >").append(LOG_FILE).append(" 2>&1 &\n");
        sb.append("echo started\n");

        return sb.toString();
    }

    /**
     * 启动 Agent（需要 Shizuku 已授权）。返回 true 表示脚本执行成功。
     */
    public static boolean start(Context ctx, AgentConfig cfg) throws Exception {
        File root = externalRoot(ctx);
        File binFile = new File(root, "bin/" + AGENT_NAME);
        File libFile = new File(root, "lib/" + LIB_NAME);
        // 每次启动都强制重新释放，确保升级 APK 后 agent 二进制同步更新
        extract(ctx);
        String script = buildStartScript(ctx, cfg, binFile, libFile);

        File scriptFile = new File(root, "start.sh");
        FileOutputStream fos = new FileOutputStream(scriptFile);
        try {
            fos.write(script.getBytes("UTF-8"));
        } finally {
            fos.close();
        }

        int code = ShizukuLauncher.exec("sh " + q(scriptFile.getAbsolutePath()));
        return code == 0;
    }

    /** 停止 Agent。 */
    public static void stop() throws Exception {
        ShizukuLauncher.exec("pkill -f cloudphone-agent; pkill -f com.genymobile.scrcpy; true");
    }

    /** 查询 Agent 是否在运行。 */
    public static boolean isRunning() {
        try {
            String out = ShizukuLauncher.execOut("ps -A | grep cloudphone-agent | grep -v grep");
            return out != null && out.length() > 0;
        } catch (Exception e) {
            return false;
        }
    }

    /** 最近一次启动日志。 */
    public static String logTail() {
        try {
            return ShizukuLauncher.execOut("tail -n 50 " + LOG_FILE + " 2>/dev/null || echo '(no log)'");
        } catch (Exception e) {
            return "(read log failed)";
        }
    }

    /** 清空日志。 */
    public static void clearLog() {
        try {
            ShizukuLauncher.exec("truncate -s 0 " + LOG_FILE + " 2>/dev/null; true");
        } catch (Exception ignored) {
        }
    }
}
