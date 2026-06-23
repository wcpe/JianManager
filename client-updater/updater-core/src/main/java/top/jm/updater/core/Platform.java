package top.jm.updater.core;

import java.util.Locale;

/**
 * 本机平台识别（契约 §2 platform）。updater 只取 {@code platform==本机} 或 {@code platform==null} 的文件。
 */
enum Platform {
    WINDOWS, MACOS, LINUX, OTHER;

    /** 由 {@code os.name} 推断本机平台。 */
    static Platform current() {
        String os = System.getProperty("os.name", "").toLowerCase(Locale.ROOT);
        if (os.contains("win")) {
            return WINDOWS;
        }
        if (os.contains("mac") || os.contains("darwin")) {
            return MACOS;
        }
        if (os.contains("nux") || os.contains("nix") || os.contains("aix")) {
            return LINUX;
        }
        return OTHER;
    }

    /** manifest 中的平台标签（windows/macos/linux）。 */
    String tag() {
        switch (this) {
            case WINDOWS:
                return "windows";
            case MACOS:
                return "macos";
            case LINUX:
                return "linux";
            default:
                return "other";
        }
    }

    /**
     * 该文件平台标签是否适用于本机：{@code null}（全平台）或与本机相符。
     */
    boolean matches(String fileTag) {
        if (fileTag == null) {
            return true;
        }
        return tag().equalsIgnoreCase(fileTag);
    }
}
