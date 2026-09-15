package com.genymobile.scrcpy.wrappers;

import com.genymobile.scrcpy.util.Ln;

/**
 * AidlWrappers - AIDL wrappers 统一入口
 *
 * 移植自本项目 libsys_core fork 的 wrappers AIDL 化设计。
 * 提供一组通过 ServiceManager + AIDL 直接访问系统服务的 wrapper，
 * 不依赖 Context，适用于 app_process 运行环境。
 *
 * 当前已实现：
 *  - AidlClipboardManager: 剪贴板管理（get/set/addPrimaryClipChangedListener）
 *  - AidlDisplayManager: 显示管理（getDisplayInfo/getDisplayIds）
 *
 * 待实现（参考 fork 反编译产物）：
 *  - AidlWindowManager: 窗口管理（旋转、尺寸、折叠）
 *  - AidlActivityManager: 活动管理（启动 Activity、发送广播）
 *  - AidlInputManager: 输入管理（注入事件）
 *  - AidlPowerManager: 电源管理（屏幕开关、唤醒）
 *
 * 使用方式：
 * <pre>
 * AidlWrappers wrappers = AidlWrappers.create();
 * if (wrappers.clipboard != null) {
 *     wrappers.clipboard.setText("hello");
 * }
 * </pre>
 *
 * 注意：AIDL wrappers 为可选增强，默认仍使用上游的 Context-based wrappers。
 * 当 AIDL wrapper 创建失败时，对应字段为 null，调用方应回退到默认实现。
 */
public final class AidlWrappers {

    public final AidlClipboardManager clipboard;
    public final AidlDisplayManager display;

    private AidlWrappers(AidlClipboardManager clipboard, AidlDisplayManager display) {
        this.clipboard = clipboard;
        this.display = display;
    }

    /**
     * 创建所有 AIDL wrappers
     * @return AidlWrappers 实例，部分字段可能为 null（创建失败时）
     */
    public static AidlWrappers create() {
        AidlClipboardManager clipboard = null;
        AidlDisplayManager display = null;

        try {
            clipboard = AidlClipboardManager.create();
        } catch (Exception e) {
            Ln.w("AidlWrappers: clipboard create failed", e);
        }

        try {
            display = AidlDisplayManager.create();
        } catch (Exception e) {
            Ln.w("AidlWrappers: display create failed", e);
        }

        int success = (clipboard != null ? 1 : 0) + (display != null ? 1 : 0);
        Ln.i("AidlWrappers: created " + success + "/2 wrappers (clipboard=" + (clipboard != null) + ", display=" + (display != null) + ")");

        return new AidlWrappers(clipboard, display);
    }

    /**
     * 检查是否所有 AIDL wrappers 都可用
     */
    public boolean isAllAvailable() {
        return clipboard != null && display != null;
    }
}
