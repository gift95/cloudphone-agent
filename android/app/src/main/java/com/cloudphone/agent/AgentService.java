package com.cloudphone.agent;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.util.Log;

/**
 * 前台服务：负责拉起/停止 Agent。
 * 开机时由 BootReceiver 触发；拉起动作通过 Shizuku 在 shizuku_server 中执行，
 * Agent 以 setsid 独立运行，本服务完成启动后自动停止。
 */
public class AgentService extends Service {

    private static final String TAG = "AgentService";
    private static final String CHANNEL_ID = "cloudphone_agent";
    private static final int NOTIF_ID = 1;

    public static final String ACTION_START = "com.cloudphone.agent.START";
    public static final String ACTION_STOP = "com.cloudphone.agent.STOP";

    private final Handler handler = new Handler(Looper.getMainLooper());
    private rikka.shizuku.Shizuku.OnBinderReceivedListener readyListener;

    public static void start(Context ctx) {
        Intent i = new Intent(ctx, AgentService.class).setAction(ACTION_START);
        if (Build.VERSION.SDK_INT >= 26) {
            ctx.startForegroundService(i);
        } else {
            ctx.startService(i);
        }
    }

    public static void stop(Context ctx) {
        Intent i = new Intent(ctx, AgentService.class).setAction(ACTION_STOP);
        ctx.startService(i);
    }

    @Override
    public void onCreate() {
        super.onCreate();
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel ch = new NotificationChannel(
                    CHANNEL_ID, "Agent 服务", NotificationManager.IMPORTANCE_LOW);
            NotificationManager nm = getSystemService(NotificationManager.class);
            if (nm != null) nm.createNotificationChannel(ch);
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent != null ? intent.getAction() : ACTION_START;
        startForeground(NOTIF_ID, buildNotification("云手机 Agent"));
        if (ACTION_STOP.equals(action)) {
            doStop();
        } else {
            doStart();
        }
        return START_NOT_STICKY;
    }

    private void doStart() {
        if (!ShizukuLauncher.available()) {
            // Shizuku 尚未就绪（开机后 Shizuku 自身也在启动中），监听 binder 到达后重试
            updateNotification("等待 Shizuku 就绪…");
            readyListener = ShizukuLauncher.addReadyListener(() -> {
                if (ShizukuLauncher.granted()) {
                    launch();
                } else {
                    updateNotification("未授权 Shizuku，请打开应用授权");
                    handler.postDelayed(this::finishSelf, 1500);
                }
            });
            handler.postDelayed(() -> {
                // 兜底：等待 60s 仍无 Shizuku 则放弃
                if (ShizukuLauncher.available() && ShizukuLauncher.granted()) {
                    launch();
                } else {
                    updateNotification("Shizuku 未就绪，Agent 未启动");
                    finishSelf();
                }
            }, 60_000);
        } else if (ShizukuLauncher.granted()) {
            launch();
        } else {
            updateNotification("未授权 Shizuku，请打开应用授权");
            handler.postDelayed(this::finishSelf, 3000);
        }
    }

    private void launch() {
        new Thread(() -> {
            try {
                AgentConfig cfg = new AgentConfig(this);
                boolean ok = AgentBootstrap.start(this, cfg);
                String status = ok ? "Agent 已启动（signaling: "
                        + cfg.get(AgentConfig.KEY_SIGNALING, "") + "）" : "启动失败，请查看日志";
                Log.i(TAG, status);
                updateNotification(status);
            } catch (Exception e) {
                Log.e(TAG, "launch failed", e);
                updateNotification("启动异常: " + e.getMessage());
            }
            handler.postDelayed(this::finishSelf, 2500);
        }, "agent-launch").start();
    }

    private void doStop() {
        new Thread(() -> {
            try {
                AgentBootstrap.stop();
                updateNotification("Agent 已停止");
            } catch (Exception e) {
                Log.e(TAG, "stop failed", e);
            }
            handler.postDelayed(this::finishSelf, 1500);
        }, "agent-stop").start();
    }

    private void finishSelf() {
        if (readyListener != null) {
            ShizukuLauncher.removeReadyListener(readyListener);
            readyListener = null;
        }
        handler.removeCallbacksAndMessages(null);
        stopForeground(STOP_FOREGROUND_REMOVE);
        stopSelf();
    }

    @Override
    public void onDestroy() {
        super.onDestroy();
        if (readyListener != null) {
            ShizukuLauncher.removeReadyListener(readyListener);
            readyListener = null;
        }
        handler.removeCallbacksAndMessages(null);
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    private Notification buildNotification(String text) {
        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return b.setContentTitle("云手机 Agent")
                .setContentText(text)
                .setSmallIcon(R.drawable.ic_launcher)
                .setOngoing(true)
                .build();
    }

    private void updateNotification(String text) {
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm != null) {
            nm.notify(NOTIF_ID, buildNotification(text));
        }
    }
}
