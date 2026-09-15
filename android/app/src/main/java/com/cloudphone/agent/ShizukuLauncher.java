package com.cloudphone.agent;

import android.app.Activity;
import android.content.pm.PackageManager;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.lang.reflect.Method;

import rikka.shizuku.Shizuku;
import rikka.shizuku.ShizukuRemoteProcess;

/**
 * Shizuku 封装：以 shell 权限执行命令，无需 root。
 * <p>
 * Shizuku API 13.x 中 {@code Shizuku.newProcess} 为私有方法（计划在 API 14 移除），
 * 通过反射调用；返回的 {@link ShizukuRemoteProcess} 是公开的 {@link Process} 子类，
 * 进程运行于 shizuku_server 中（shell uid），与本应用进程生命周期无关。
 * </p>
 * 需要设备上安装 Shizuku（ADB 激活或 root 激活）并为本应用授权。
 */
public final class ShizukuLauncher {

    private static final String TAG = "ShizukuLauncher";
    public static final int REQ_PERMISSION = 1001;

    private ShizukuLauncher() {
    }

    /** Shizuku 服务是否在线（binder 已连接）。 */
    public static boolean available() {
        try {
            return Shizuku.pingBinder();
        } catch (Throwable t) {
            return false;
        }
    }

    /** 本应用是否已被授予 Shizuku 权限。 */
    public static boolean granted() {
        try {
            return Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED;
        } catch (Throwable t) {
            return false;
        }
    }

    /** 弹出系统授权对话框，结果回调到 Activity.onRequestPermissionsResult(code == REQ_PERMISSION)。 */
    public static void requestPermission(Activity activity) {
        try {
            if (Shizuku.isPreV11()) {
                // 系统签名模式（Android 6/7 的 Shizuku），默认已授权
                return;
            }
            Shizuku.requestPermission(REQ_PERMISSION);
        } catch (Throwable t) {
            Log.e(TAG, "requestPermission failed", t);
        }
    }

    /** 注册 Shizuku 就绪监听（binder 到达时触发，粘性），返回监听器实例以便移除。 */
    public static Shizuku.OnBinderReceivedListener addReadyListener(final Runnable r) {
        Shizuku.OnBinderReceivedListener listener = new Shizuku.OnBinderReceivedListener() {
            @Override
            public void onBinderReceived() {
                r.run();
            }
        };
        try {
            Shizuku.addBinderReceivedListenerSticky(listener);
        } catch (Throwable t) {
            Log.e(TAG, "addBinderReceivedListenerSticky failed", t);
        }
        return listener;
    }

    public static void removeReadyListener(Shizuku.OnBinderReceivedListener listener) {
        if (listener == null) return;
        try {
            Shizuku.removeBinderReceivedListener(listener);
        } catch (Throwable t) {
            Log.e(TAG, "removeBinderReceivedListener failed", t);
        }
    }

    /** 反射调用 Shizuku.newProcess(String[] cmd, String[] env, String dir)。 */
    private static ShizukuRemoteProcess newProcess(String[] cmd, String[] env, String dir) throws Exception {
        Method m = Shizuku.class.getDeclaredMethod("newProcess", String[].class, String[].class, String.class);
        m.setAccessible(true);
        return (ShizukuRemoteProcess) m.invoke(null, (Object) cmd, (Object) env, (Object) dir);
    }

    /**
     * 通过 Shizuku 以 shell 身份执行命令（运行于 shizuku_server，独立于本进程）。
     *
     * @return 命令退出码
     */
    public static int exec(String cmd) throws Exception {
        Log.d(TAG, "exec: " + cmd);
        ShizukuRemoteProcess proc = newProcess(new String[]{"sh", "-c", cmd}, null, null);
        // 双线程读取 stdout / stderr，避免管道写满死锁
        ByteArrayOutputStream outBuf = new ByteArrayOutputStream();
        ByteArrayOutputStream errBuf = new ByteArrayOutputStream();
        Thread t1 = new Thread(() -> pump(proc.getInputStream(), outBuf), "shizuku-stdout");
        Thread t2 = new Thread(() -> pump(proc.getErrorStream(), errBuf), "shizuku-stderr");
        t1.start();
        t2.start();
        int code = proc.waitFor();
        t1.join(3000);
        t2.join(3000);
        String out = outBuf.toString();
        if (out.length() > 0) Log.d(TAG, "stdout: " + out);
        String err = errBuf.toString();
        if (err.length() > 0) Log.w(TAG, "stderr: " + err);
        return code;
    }

    /** 执行命令并返回标准输出内容。 */
    public static String execOut(String cmd) throws Exception {
        ShizukuRemoteProcess proc = newProcess(new String[]{"sh", "-c", cmd}, null, null);
        ByteArrayOutputStream outBuf = new ByteArrayOutputStream();
        ByteArrayOutputStream errBuf = new ByteArrayOutputStream();
        Thread t1 = new Thread(() -> pump(proc.getInputStream(), outBuf), "shizuku-stdout");
        Thread t2 = new Thread(() -> pump(proc.getErrorStream(), errBuf), "shizuku-stderr");
        t1.start();
        t2.start();
        proc.waitFor();
        t1.join(3000);
        t2.join(3000);
        return outBuf.toString().trim();
    }

    private static void pump(InputStream in, ByteArrayOutputStream out) {
        if (in == null) return;
        byte[] buf = new byte[4096];
        try {
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
        } catch (IOException e) {
            // 忽略读取结束
        } finally {
            try {
                in.close();
            } catch (IOException ignored) {
            }
        }
    }
}
