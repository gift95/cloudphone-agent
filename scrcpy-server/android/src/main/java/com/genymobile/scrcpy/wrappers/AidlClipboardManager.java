package com.genymobile.scrcpy.wrappers;

import android.content.ClipData;
import android.content.IOnPrimaryClipChangedListener;
import android.os.IBinder;

import com.genymobile.scrcpy.util.Ln;

import java.lang.reflect.Method;

/**
 * AidlClipboardManager - AIDL 版本剪贴板管理器
 *
 * 移植自本项目 libsys_core fork 的 wrappers AIDL 化设计：
 *  - 上游 scrcpy 通过 Context.getSystemService(CLIPBOARD_SERVICE) 获取 ClipboardManager；
 *  - fork 改用 ServiceManager.getService("clipboard") 获取 IBinder，通过 AIDL 接口直接调用；
 *  - 目的：在 app_process 环境中不依赖 Context，避免部分设备 Context.getSystemService 返回 null。
 *
 * 注意：ServiceManager 和 IClipboard 都是隐藏 API，通过反射访问。
 * IOnPrimaryClipChangedListener.aidl 已在 src/main/aidl/android/content/ 中定义。
 */
public final class AidlClipboardManager {

    private static final String SERVICE_NAME = "clipboard";

    private final Object clipboardService; // IClipboard
    private final Method getPrimaryClipMethod;
    private final Method setPrimaryClipMethod;
    private final Method addPrimaryClipChangedListenerMethod;

    private AidlClipboardManager(Object clipboardService,
                                  Method getPrimaryClipMethod,
                                  Method setPrimaryClipMethod,
                                  Method addPrimaryClipChangedListenerMethod) {
        this.clipboardService = clipboardService;
        this.getPrimaryClipMethod = getPrimaryClipMethod;
        this.setPrimaryClipMethod = setPrimaryClipMethod;
        this.addPrimaryClipChangedListenerMethod = addPrimaryClipChangedListenerMethod;
    }

    /**
     * 通过反射获取 ServiceManager.getService 方法
     */
    private static IBinder getService(String name) throws Exception {
        Class<?> serviceManagerClass = Class.forName("android.os.ServiceManager");
        // Android 高版本 getService 可能需要两个参数，尝试不同签名
        try {
            Method getService = serviceManagerClass.getMethod("getService", String.class);
            return (IBinder) getService.invoke(null, name);
        } catch (NoSuchMethodException e) {
            // 尝试 getService(String, String) 签名
            Method getService = serviceManagerClass.getMethod("getService", String.class, String.class);
            return (IBinder) getService.invoke(null, name, null);
        }
    }

    /**
     * 通过 ServiceManager + AIDL 创建剪贴板管理器
     * @return AidlClipboardManager，失败时返回 null
     */
    public static AidlClipboardManager create() {
        try {
            IBinder binder = getService(SERVICE_NAME);
            if (binder == null) {
                Ln.w("AidlClipboardManager: clipboard service not found");
                return null;
            }

            // 通过反射获取 IClipboard 接口（android.content.IClipboard 是隐藏 API）
            Class<?> clipboardClass = Class.forName("android.content.IClipboard$Stub");
            Method asInterface = clipboardClass.getMethod("asInterface", IBinder.class);
            Object clipboardService = asInterface.invoke(null, binder);

            if (clipboardService == null) {
                Ln.w("AidlClipboardManager: failed to get IClipboard interface");
                return null;
            }

            // 获取方法引用（不同 Android 版本参数可能不同）
            Class<?> serviceClass = clipboardService.getClass();
            Method getPrimaryClip = findMethod(serviceClass, "getPrimaryClip");
            Method setPrimaryClip = findMethod(serviceClass, "setPrimaryClip");
            Method addListener = findMethod(serviceClass, "addPrimaryClipChangedListener");

            if (getPrimaryClip == null || setPrimaryClip == null || addListener == null) {
                Ln.w("AidlClipboardManager: failed to find IClipboard methods");
                return null;
            }

            Ln.i("AidlClipboardManager: created via AIDL (ServiceManager)");
            return new AidlClipboardManager(clipboardService, getPrimaryClip, setPrimaryClip, addListener);
        } catch (Exception e) {
            Ln.e("AidlClipboardManager: create failed, fallback to Context-based", e);
            return null;
        }
    }

    /**
     * 查找方法（忽略参数签名，取第一个匹配的）
     */
    private static Method findMethod(Class<?> clazz, String name) {
        for (Method m : clazz.getMethods()) {
            if (m.getName().equals(name)) {
                m.setAccessible(true);
                return m;
            }
        }
        return null;
    }

    /**
     * 调用方法（自动适配参数数量）
     */
    private Object invokeMethod(Method method, Object... args) throws Exception {
        Class<?>[] paramTypes = method.getParameterTypes();
        Object[] actualArgs = new Object[paramTypes.length];
        for (int i = 0; i < paramTypes.length && i < args.length; i++) {
            actualArgs[i] = args[i];
        }
        // 剩余参数用 null 或默认值填充
        for (int i = args.length; i < paramTypes.length; i++) {
            actualArgs[i] = getDefaultValue(paramTypes[i]);
        }
        return method.invoke(clipboardService, actualArgs);
    }

    private static Object getDefaultValue(Class<?> type) {
        if (type == String.class) return "com.android.shell";
        if (type == int.class) return 0;
        if (type == boolean.class) return false;
        return null;
    }

    /**
     * 获取剪贴板文本
     */
    public CharSequence getText() {
        try {
            ClipData clipData = (ClipData) invokeMethod(getPrimaryClipMethod);
            if (clipData == null || clipData.getItemCount() == 0) {
                return null;
            }
            return clipData.getItemAt(0).getText();
        } catch (Exception e) {
            Ln.e("AidlClipboardManager: getText failed", e);
            return null;
        }
    }

    /**
     * 设置剪贴板文本
     */
    public boolean setText(CharSequence text) {
        try {
            ClipData clipData = ClipData.newPlainText(null, text);
            invokeMethod(setPrimaryClipMethod, clipData);
            return true;
        } catch (Exception e) {
            Ln.e("AidlClipboardManager: setText failed", e);
            return false;
        }
    }

    /**
     * 添加剪贴板变化监听器
     */
    public void addPrimaryClipChangedListener(IOnPrimaryClipChangedListener listener) {
        try {
            invokeMethod(addPrimaryClipChangedListenerMethod, listener);
        } catch (Exception e) {
            Ln.e("AidlClipboardManager: addPrimaryClipChangedListener failed", e);
        }
    }
}
