package top.wcpe.mc.jm.updater.core;

import java.io.InputStream;
import java.util.Properties;

/** updater-core 构建元信息，随 jar 内资源打包。 */
final class BuildInfo {

    static final String RESOURCE = "META-INF/jm-updater-core.properties";
    private static final BuildInfo CURRENT = loadCurrent();

    private final String version;
    private final String gitCommit;
    private final boolean dirty;
    private final String buildTime;

    private BuildInfo(String version, String gitCommit, boolean dirty, String buildTime) {
        this.version = emptyToUnknown(version);
        this.gitCommit = emptyToUnknown(gitCommit);
        this.dirty = dirty;
        this.buildTime = buildTime == null ? "" : buildTime.trim();
    }

    static BuildInfo current() {
        return CURRENT;
    }

    static BuildInfo fromProperties(Properties p) {
        if (p == null) {
            p = new Properties();
        }
        return new BuildInfo(
                p.getProperty("version"),
                p.getProperty("gitCommit"),
                Boolean.parseBoolean(p.getProperty("dirty", "false")),
                p.getProperty("buildTime"));
    }

    String version() {
        return version;
    }

    String gitCommit() {
        return gitCommit;
    }

    boolean dirty() {
        return dirty;
    }

    String buildTime() {
        return buildTime;
    }

    String display() {
        if ("unknown".equals(version)) {
            return dirty ? "unknown.dirty" : "unknown";
        }
        if ("unknown".equals(gitCommit)) {
            return dirty ? version + "+dirty" : version;
        }
        String display = version + "+" + gitCommit;
        if (dirty) {
            display += ".dirty";
        }
        return display;
    }

    private static BuildInfo loadCurrent() {
        Properties p = new Properties();
        InputStream in = BuildInfo.class.getClassLoader().getResourceAsStream(RESOURCE);
        if (in == null) {
            return fromProperties(p);
        }
        try {
            p.load(in);
            return fromProperties(p);
        } catch (Exception e) {
            return fromProperties(new Properties());
        } finally {
            try {
                in.close();
            } catch (Exception ignored) {
                // 关闭失败不影响更新器启动。
            }
        }
    }

    private static String emptyToUnknown(String s) {
        if (s == null || s.trim().isEmpty()) {
            return "unknown";
        }
        return s.trim();
    }
}
