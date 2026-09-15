package com.genymobile.scrcpy.wrappers;

import android.os.IBinder;

import com.genymobile.scrcpy.util.Ln;

import java.lang.reflect.Method;

/**
 * AidlDisplayManager - AIDL 版本显示管理器
 *
 * 移植自本项目 libsys_core fork 的 wrappers AIDL 化设计：
 *  - 上游通过 Context.getSystemService(DISPLAY_SERVICE) 获取 DisplayManager；
 *  - fork 改用 ServiceManager.getService("display") 获取 IDisplayManager AIDL 接口；
 *  - 目的：在 app_process 环境中不依赖 Context，支持旋转、显示控制等操作。
 *
 * 注意：ServiceManager 和 IDisplayManager 都是隐藏 API，通过反射访问。
 * IDisplayWindowListener.aidl 已在 src/main/aidl/android/view/ 中定义。
 */
public final class AidlDisplayManager {

    private static final String SERVICE_NAME = "display";

    private final Object displayManager; // IDisplayManager
    private final Method getDisplayInfoMethod;
    private final Method getDisplaysMethod;

    private AidlDisplayManager(Object displayManager,
                                Method getDisplayInfoMethod,
                                Method getDisplaysMethod) {
        this.displayManager = displayManager;
        this.getDisplayInfoMethod = getDisplayInfoMethod;
        this.getDisplaysMethod = getDisplaysMethod;
    }

    /**
     * 通过反射获取 ServiceManager.getService 方法
     */
    private static IBinder getService(String name) throws Exception {
        Class<?> serviceManagerClass = Class.forName("android.os.ServiceManager");
        try {
            Method getService = serviceManagerClass.getMethod("getService", String.class);
            return (IBinder) getService.invoke(null, name);
        } catch (NoSuchMethodException e) {
            Method getService = serviceManagerClass.getMethod("getService", String.class, String.class);
            return (IBinder) getService.invoke(null, name, null);
        }
    }

    /**
     * 通过 ServiceManager + AIDL 创建显示管理器
     */
    public static AidlDisplayManager create() {
        try {
            IBinder binder = getService(SERVICE_NAME);
            if (binder == null) {
                Ln.w("AidlDisplayManager: display service not found");
                return null;
            }

            Class<?> displayManagerClass = Class.forName("android.hardware.display.IDisplayManager$Stub");
            Method asInterface = displayManagerClass.getMethod("asInterface", IBinder.class);
            Object displayManager = asInterface.invoke(null, binder);

            if (displayManager == null) {
                Ln.w("AidlDisplayManager: failed to get IDisplayManager interface");
                return null;
            }

            Class<?> serviceClass = displayManager.getClass();
            Method getDisplayInfo = findMethod(serviceClass, "getDisplayInfo");
            Method getDisplays = findMethod(serviceClass, "getDisplayIds");

            if (getDisplayInfo == null || getDisplays == null) {
                Ln.w("AidlDisplayManager: failed to find IDisplayManager methods");
                return null;
            }

            Ln.i("AidlDisplayManager: created via AIDL (ServiceManager)");
            return new AidlDisplayManager(displayManager, getDisplayInfo, getDisplays);
        } catch (Exception e) {
            Ln.e("AidlDisplayManager: create failed", e);
            return null;
        }
    }

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
     * 获取指定显示 ID 的 DisplayInfo
     */
    public Object getDisplayInfo(int displayId) {
        try {
            return getDisplayInfoMethod.invoke(displayManager, displayId);
        } catch (Exception e) {
            Ln.e("AidlDisplayManager: getDisplayInfo failed", e);
            return null;
        }
    }

    /**
     * 获取所有显示 ID 列表
     */
    public int[] getDisplayIds() {
        try {
            Object result = getDisplaysMethod.invoke(displayManager);
            if (result instanceof int[]) {
                return (int[]) result;
            }
            return new int[0];
        } catch (Exception e) {
            Ln.e("AidlDisplayManager: getDisplayIds failed", e);
            return new int[0];
        }
    }
}
