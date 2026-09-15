// fork 差异参考：libsys_core 使用 AIDL Binder 监听显示窗口变化（替代上游 reflection）。
// 反编译证据：com.android.helper 的 android.view.IDisplayWindowListener（Stub/Proxy 完整）。
// 启用方式：本文件放至 src/main/aidl/android/view/IDisplayWindowListener.aidl 后，
//           将 wrappers/DisplayManager.java 的监听改为 registerDisplayListener 的 Binder 调用。
package android.view;

import android.content.res.Configuration;

interface IDisplayWindowListener {
    void onDisplayAdded(int displayId);
    void onDisplayConfigurationChanged(int displayId, in Configuration newConfig);
    void onDisplayRemoved(int displayId);
}
