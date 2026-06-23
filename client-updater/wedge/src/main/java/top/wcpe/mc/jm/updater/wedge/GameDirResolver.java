package top.wcpe.mc.jm.updater.wedge;

/**
 * gameDir 解析（契约 §6.1）：{@code agentArgs} 优先；为空则解析 {@code sun.java.command} 的
 * {@code --gameDir}；再不行返回 null 由调用方兜底（楔子自定位目录推断 / fail-open）。
 *
 * <p>抽为纯函数便于单测（不依赖 JVM 真实 {@code sun.java.command}）。
 */
final class GameDirResolver {

    private GameDirResolver() {
    }

    /**
     * 解析 gameDir。
     *
     * @param agentArgs   {@code -javaagent:wedge.jar=<gameDir>} 传入的参数（可能为 null/空）
     * @param javaCommand {@code System.getProperty("sun.java.command")}（可能为 null）
     * @return gameDir 字符串；无法确定返回 null
     */
    static String resolve(String agentArgs, String javaCommand) {
        // 1. agentArgs 优先（契约 §6.1）。
        if (agentArgs != null) {
            String trimmed = agentArgs.trim();
            if (!trimmed.isEmpty()) {
                return stripQuotes(trimmed);
            }
        }
        // 2. 兜底：解析 sun.java.command 的 --gameDir <值> 或 --gameDir=<值>。
        if (javaCommand != null) {
            String fromCmd = parseGameDirFlag(javaCommand);
            if (fromCmd != null) {
                return fromCmd;
            }
        }
        return null;
    }

    /** 从命令行字符串解析 {@code --gameDir <值>} / {@code --gameDir=<值>}（值可带引号、可含空格当被引号包裹）。 */
    static String parseGameDirFlag(String command) {
        String flag = "--gameDir";
        int idx = command.indexOf(flag);
        if (idx < 0) {
            return null;
        }
        int after = idx + flag.length();
        if (after >= command.length()) {
            return null;
        }
        char sep = command.charAt(after);
        if (sep == '=') {
            return readValue(command, after + 1);
        }
        if (sep == ' ' || sep == '\t') {
            // 跳过空白到值起始。
            int i = after;
            while (i < command.length() && (command.charAt(i) == ' ' || command.charAt(i) == '\t')) {
                i++;
            }
            return readValue(command, i);
        }
        // --gameDirXxx：非本 flag。
        return null;
    }

    /** 从 start 读一个值：带引号读到配对引号，否则读到下一个空白。 */
    private static String readValue(String s, int start) {
        if (start >= s.length()) {
            return null;
        }
        char c = s.charAt(start);
        if (c == '"' || c == '\'') {
            int end = s.indexOf(c, start + 1);
            if (end < 0) {
                return s.substring(start + 1); // 未闭合，取到末尾。
            }
            return s.substring(start + 1, end);
        }
        int i = start;
        while (i < s.length() && s.charAt(i) != ' ' && s.charAt(i) != '\t') {
            i++;
        }
        String v = s.substring(start, i);
        return v.isEmpty() ? null : v;
    }

    private static String stripQuotes(String s) {
        if (s.length() >= 2) {
            char first = s.charAt(0);
            char last = s.charAt(s.length() - 1);
            if ((first == '"' && last == '"') || (first == '\'' && last == '\'')) {
                return s.substring(1, s.length() - 1);
            }
        }
        return s;
    }
}
