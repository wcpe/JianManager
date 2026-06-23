package top.jm.updater.wedge;

import java.util.Locale;

/**
 * 玩家面向提示文案 i18n（契约 FR-089「玩家面向提示文案 i18n」）。
 *
 * <p>按 {@code user.language} 选中/英（其余回退英文）。楔子保持零依赖，故用内联表而非
 * ResourceBundle 文件（避免打包资源路径在 javaagent 下的不确定性）。
 */
final class Messages {

    enum Key {
        STARTING,
        UPDATE_OK,
        UPDATE_FAILED_STATIC,
        UPDATE_TIMEOUT,
        WEDGE_FAILOPEN
    }

    private final boolean zh;

    private Messages(boolean zh) {
        this.zh = zh;
    }

    /** 按 JVM 默认语言创建。 */
    static Messages forDefaultLocale() {
        return forLanguage(Locale.getDefault().getLanguage());
    }

    /** 按语言码创建（便于单测）。 */
    static Messages forLanguage(String lang) {
        return new Messages("zh".equalsIgnoreCase(lang));
    }

    String get(Key key) {
        switch (key) {
            case STARTING:
                return zh ? "[JM 更新器] 正在检查客户端更新…"
                          : "[JM Updater] Checking for client updates...";
            case UPDATE_OK:
                return zh ? "[JM 更新器] 客户端已是最新，正在启动游戏。"
                          : "[JM Updater] Client up to date, launching game.";
            case UPDATE_FAILED_STATIC:
                return zh ? "[JM 更新器] 更新未完成，将以当前本地版本启动游戏（可联网后重试）。"
                          : "[JM Updater] Update incomplete; launching with current local version "
                            + "(retry when online).";
            case UPDATE_TIMEOUT:
                return zh ? "[JM 更新器] 更新超时，将以当前本地版本启动游戏。"
                          : "[JM Updater] Update timed out; launching with current local version.";
            case WEDGE_FAILOPEN:
                return zh ? "[JM 更新器] 更新器自身异常，已跳过更新直接启动游戏。"
                          : "[JM Updater] Updater error; skipped update and launching game.";
            default:
                return "";
        }
    }
}
