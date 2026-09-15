// fork 差异参考：libsys_core 使用 AIDL Binder 监听剪贴板变化（替代上游 reflection）。
// 反编译证据：com.android.helper 的 android.content.IOnPrimaryClipChangedListener（Stub/Proxy 完整）。
// 启用方式：本文件放至 src/main/aidl/android/content/IOnPrimaryClipChangedListener.aidl 后，
//           将 wrappers/ClipboardManager.java 的监听改为 addPrimaryClipChangedListener 的 Binder 调用。
package android.content;

interface IOnPrimaryClipChangedListener {
    void dispatchPrimaryClipChanged();
}
