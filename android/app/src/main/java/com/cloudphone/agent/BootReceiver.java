package com.cloudphone.agent;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/**
 * 开机自启：系统启动完成后（或应用升级后）若开启了自动启动，
 * 则通过 AgentService 拉起 Agent。
 */
public class BootReceiver extends BroadcastReceiver {

    @Override
    public void onReceive(Context context, Intent intent) {
        String action = intent != null ? intent.getAction() : null;
        if (Intent.ACTION_BOOT_COMPLETED.equals(action)
                || Intent.ACTION_MY_PACKAGE_REPLACED.equals(action)) {
            AgentConfig cfg = new AgentConfig(context);
            if (cfg.getBool(AgentConfig.KEY_AUTOSTART, true)) {
                AgentService.start(context);
            }
        }
    }
}
