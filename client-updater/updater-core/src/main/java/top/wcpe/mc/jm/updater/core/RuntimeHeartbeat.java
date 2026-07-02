package top.wcpe.mc.jm.updater.core;

import java.util.LinkedHashMap;
import java.util.Map;

/** 客户端启动运行态心跳构建（FR-265）。 */
final class RuntimeHeartbeat {

    private RuntimeHeartbeat() {
    }

    static String build(String coreVersion, long localVersion) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("platform", System.getProperty("os.name", ""));
        m.put("javaVersion", System.getProperty("java.version", ""));
        m.put("launcher", Telemetry.launcher());
        m.put("coreVersion", coreVersion == null ? "" : coreVersion);
        m.put("localVersion", localVersion);
        return Json.canonical(m);
    }
}
