package top.wcpe.mc.jm.updater.core;

import java.util.Locale;

/**
 * core 侧玩家面向文案 i18n（FR-099 进度窗口）。
 *
 * <p>按 {@code user.language} 选中/英（其余回退英文），与 wedge {@code Messages} 同构。
 * 数字格式（字节/速度/时长）用 {@link Locale#US} 保证小数点稳定，不随系统区域变。
 */
final class CoreMessages {

    private final boolean zh;

    private CoreMessages(boolean zh) {
        this.zh = zh;
    }

    /** 按 JVM 默认语言创建。 */
    static CoreMessages forDefaultLocale() {
        return forLanguage(Locale.getDefault().getLanguage());
    }

    /** 按语言码创建（便于单测）。 */
    static CoreMessages forLanguage(String lang) {
        return new CoreMessages("zh".equalsIgnoreCase(lang));
    }

    String title() {
        return zh ? "JM 客户端更新" : "JM Client Update";
    }

    String preparing() {
        return zh ? "正在准备更新…" : "Preparing update...";
    }

    String downloading() {
        return zh ? "正在下载更新…" : "Downloading update...";
    }

    String complete() {
        return zh ? "更新完成，正在启动游戏…" : "Update complete, launching game...";
    }

    /** ETA 文案；{@code secs < 0} 表示未知（计算中）。 */
    String eta(long secs) {
        if (secs < 0) {
            return zh ? "剩余：计算中" : "ETA: --";
        }
        return (zh ? "剩余：" : "ETA: ") + humanDuration(secs);
    }

    /** 人类可读字节，如 {@code 3.2 MB}（{@link Locale#US} 小数点）。 */
    static String humanBytes(long bytes) {
        if (bytes < 1024) {
            return bytes + " B";
        }
        double kb = bytes / 1024.0;
        if (kb < 1024) {
            return String.format(Locale.US, "%.1f KB", kb);
        }
        double mb = kb / 1024.0;
        if (mb < 1024) {
            return String.format(Locale.US, "%.1f MB", mb);
        }
        return String.format(Locale.US, "%.2f GB", mb / 1024.0);
    }

    /** 人类可读速度，如 {@code 3.2 MB/s}。 */
    static String humanSpeed(long bytesPerSec) {
        return humanBytes(bytesPerSec) + "/s";
    }

    /** 人类可读时长，如 {@code 12s} / {@code 2m 05s}。 */
    static String humanDuration(long secs) {
        if (secs < 0) {
            return "--";
        }
        if (secs < 60) {
            return secs + "s";
        }
        long m = secs / 60;
        long s = secs % 60;
        return String.format(Locale.US, "%dm %02ds", m, s);
    }
}
