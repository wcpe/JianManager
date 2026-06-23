package top.wcpe.mc.jm.updater.wedge;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 楔子专用极简 JSON 读取（只解析扁平对象 {@code jm-updater.json}：channel/key/endpoint/coreJar/timeoutSec）。
 *
 * <p>楔子须零三方依赖、Java 8 兼容（先于 mod loader 加载），故不复用 updater-core 的 JSON。
 * 仅支持字符串值与数字值的一层对象；足够读配置，刻意不做完整 JSON（YAGNI）。
 */
final class MiniJson {

    private MiniJson() {
    }

    /** 解析扁平对象为 {@code String→String}（数字也转字符串）。失败抛 {@link IllegalArgumentException}。 */
    static Map<String, String> parseFlatObject(String text) {
        Map<String, String> out = new LinkedHashMap<String, String>();
        int i = 0;
        int n = text.length();
        i = skipWs(text, i);
        if (i >= n || text.charAt(i) != '{') {
            throw new IllegalArgumentException("期望对象起始 '{'");
        }
        i++;
        i = skipWs(text, i);
        if (i < n && text.charAt(i) == '}') {
            return out;
        }
        while (i < n) {
            i = skipWs(text, i);
            if (text.charAt(i) != '"') {
                throw new IllegalArgumentException("期望键的引号，位置 " + i);
            }
            int[] keyEnd = new int[1];
            String key = readString(text, i, keyEnd);
            i = keyEnd[0];
            i = skipWs(text, i);
            if (i >= n || text.charAt(i) != ':') {
                throw new IllegalArgumentException("期望 ':'，位置 " + i);
            }
            i++;
            i = skipWs(text, i);
            String value;
            if (text.charAt(i) == '"') {
                int[] valEnd = new int[1];
                value = readString(text, i, valEnd);
                i = valEnd[0];
            } else {
                int start = i;
                while (i < n && "}, \t\r\n".indexOf(text.charAt(i)) < 0) {
                    i++;
                }
                value = text.substring(start, i).trim();
            }
            out.put(key, value);
            i = skipWs(text, i);
            if (i >= n) {
                break;
            }
            char c = text.charAt(i++);
            if (c == '}') {
                return out;
            }
            if (c != ',') {
                throw new IllegalArgumentException("期望 ',' 或 '}'，位置 " + (i - 1));
            }
        }
        return out;
    }

    private static String readString(String s, int start, int[] endOut) {
        StringBuilder sb = new StringBuilder();
        int i = start + 1; // 跳过起始 "
        int n = s.length();
        while (i < n) {
            char c = s.charAt(i++);
            if (c == '"') {
                endOut[0] = i;
                return sb.toString();
            }
            if (c == '\\' && i < n) {
                char esc = s.charAt(i++);
                switch (esc) {
                    case '"': sb.append('"'); break;
                    case '\\': sb.append('\\'); break;
                    case '/': sb.append('/'); break;
                    case 'n': sb.append('\n'); break;
                    case 'r': sb.append('\r'); break;
                    case 't': sb.append('\t'); break;
                    case 'u':
                        sb.append((char) Integer.parseInt(s.substring(i, i + 4), 16));
                        i += 4;
                        break;
                    default: sb.append(esc);
                }
            } else {
                sb.append(c);
            }
        }
        throw new IllegalArgumentException("字符串未闭合");
    }

    private static int skipWs(String s, int i) {
        int n = s.length();
        while (i < n) {
            char c = s.charAt(i);
            if (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
                i++;
            } else {
                break;
            }
        }
        return i;
    }
}
