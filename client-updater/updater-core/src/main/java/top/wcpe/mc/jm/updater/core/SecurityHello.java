package top.wcpe.mc.jm.updater.core;

import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.TimeZone;

/** 启动安全画像 hello：必要安全日志，不受诊断遥测开关影响。 */
final class SecurityHello {

    private SecurityHello() {
    }

    static String buildBody(SecurityIdentity identity) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("channel", identity.channel);
        body.put("playerName", identity.playerName);
        body.put("machineId", identity.machineId);
        body.put("installId", identity.installId);
        body.put("coreVersion", identity.coreVersion);
        body.put("wedgeVersion", identity.wedgeVersion);
        body.put("manifestVersion", identity.manifestVersion);
        body.put("os", System.getProperty("os.name", ""));
        body.put("osVersion", System.getProperty("os.version", ""));
        body.put("arch", System.getProperty("os.arch", ""));
        body.put("javaVendor", System.getProperty("java.vendor", ""));
        body.put("javaVersion", System.getProperty("java.version", ""));
        body.put("javaArch", System.getProperty("os.arch", ""));
        body.put("launcher", Telemetry.launcher());
        body.put("locale", Locale.getDefault().toLanguageTag());
        body.put("timezone", TimeZone.getDefault().getID());
        body.put("memoryTier", memoryTier());
        return Json.canonical(body);
    }

    static void postBestEffort(Transport transport, SecurityIdentity identity) {
        try {
            transport.postSecurityHello(buildBody(identity));
        } catch (Throwable t) {
            // hello 是 best-effort 安全画像：失败不得阻断更新或启动。
        }
    }

    private static String memoryTier() {
        long maxMb = Runtime.getRuntime().maxMemory() / 1024L / 1024L;
        if (maxMb < 4096L) {
            return "<4G";
        }
        if (maxMb < 8192L) {
            return "4-8G";
        }
        if (maxMb < 16384L) {
            return "8-16G";
        }
        return ">=16G";
    }
}
