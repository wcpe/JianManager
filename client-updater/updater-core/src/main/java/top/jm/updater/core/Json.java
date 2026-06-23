package top.jm.updater.core;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * 极简零依赖 JSON 解析 + 序列化（契约约束 updater-core 仅用 JDK 自带能力 + 轻量 JSON）。
 *
 * <p>解析产出 Java 原生类型：{@code Map<String,Object>} / {@code List<Object>} /
 * {@code String} / {@code Double} / {@code Long} / {@code Boolean} / {@code null}。
 * 另提供 {@link #canonical(Object)}：键按 UTF-8 码点升序递归排序、无多余空白、数字最简形式，
 * 用于 manifest 验签前的 canonical JSON 还原（契约 §3）。
 */
final class Json {

    private Json() {
    }

    /** 解析 JSON 文本为原生对象树。 */
    static Object parse(String text) {
        Parser p = new Parser(text);
        p.skipWs();
        Object v = p.readValue();
        p.skipWs();
        if (!p.eof()) {
            throw new JsonException("解析后存在多余字符，位置 " + p.pos);
        }
        return v;
    }

    /**
     * Canonical JSON 序列化：对象键按字符串自然序（UTF-16 码元升序，与 ASCII/常见键的 UTF-8 码点序一致）
     * 递归排序、无多余空白。供验签时还原签名范围（契约 §3）。
     */
    static String canonical(Object value) {
        StringBuilder sb = new StringBuilder();
        writeCanonical(value, sb);
        return sb.toString();
    }

    @SuppressWarnings("unchecked")
    private static void writeCanonical(Object value, StringBuilder sb) {
        if (value == null) {
            sb.append("null");
        } else if (value instanceof Map) {
            // TreeMap 保证键有序（契约要求 canonical 键升序）。
            Map<String, Object> sorted = new TreeMap<>((Map<String, Object>) value);
            sb.append('{');
            boolean first = true;
            for (Map.Entry<String, Object> e : sorted.entrySet()) {
                if (!first) {
                    sb.append(',');
                }
                first = false;
                writeString(e.getKey(), sb);
                sb.append(':');
                writeCanonical(e.getValue(), sb);
            }
            sb.append('}');
        } else if (value instanceof List) {
            sb.append('[');
            boolean first = true;
            for (Object item : (List<Object>) value) {
                if (!first) {
                    sb.append(',');
                }
                first = false;
                writeCanonical(item, sb);
            }
            sb.append(']');
        } else if (value instanceof String) {
            writeString((String) value, sb);
        } else if (value instanceof Boolean) {
            sb.append(value.toString());
        } else if (value instanceof Number) {
            sb.append(numberToString((Number) value));
        } else {
            throw new JsonException("不支持的 canonical 类型: " + value.getClass());
        }
    }

    /** 数字最简形式：整数值不带小数点/指数。 */
    private static String numberToString(Number n) {
        if (n instanceof Long || n instanceof Integer) {
            return Long.toString(n.longValue());
        }
        double d = n.doubleValue();
        if (d == Math.rint(d) && !Double.isInfinite(d) && Math.abs(d) < 1e15) {
            return Long.toString((long) d);
        }
        return Double.toString(d);
    }

    private static void writeString(String s, StringBuilder sb) {
        sb.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"':
                    sb.append("\\\"");
                    break;
                case '\\':
                    sb.append("\\\\");
                    break;
                case '\n':
                    sb.append("\\n");
                    break;
                case '\r':
                    sb.append("\\r");
                    break;
                case '\t':
                    sb.append("\\t");
                    break;
                case '\b':
                    sb.append("\\b");
                    break;
                case '\f':
                    sb.append("\\f");
                    break;
                default:
                    if (c < 0x20) {
                        sb.append(String.format("\\u%04x", (int) c));
                    } else {
                        sb.append(c);
                    }
            }
        }
        sb.append('"');
    }

    /** JSON 解析异常。 */
    static final class JsonException extends RuntimeException {
        JsonException(String message) {
            super(message);
        }
    }

    private static final class Parser {
        private final String s;
        private int pos;

        Parser(String s) {
            this.s = s;
        }

        boolean eof() {
            return pos >= s.length();
        }

        void skipWs() {
            while (pos < s.length()) {
                char c = s.charAt(pos);
                if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                    pos++;
                } else {
                    break;
                }
            }
        }

        Object readValue() {
            skipWs();
            if (eof()) {
                throw new JsonException("意外的输入结束");
            }
            char c = s.charAt(pos);
            switch (c) {
                case '{':
                    return readObject();
                case '[':
                    return readArray();
                case '"':
                    return readString();
                case 't':
                case 'f':
                    return readBoolean();
                case 'n':
                    return readNull();
                default:
                    return readNumber();
            }
        }

        private Map<String, Object> readObject() {
            Map<String, Object> map = new LinkedHashMap<>();
            expect('{');
            skipWs();
            if (peek() == '}') {
                pos++;
                return map;
            }
            while (true) {
                skipWs();
                if (peek() != '"') {
                    throw new JsonException("对象键必须是字符串，位置 " + pos);
                }
                String key = readString();
                skipWs();
                expect(':');
                Object value = readValue();
                map.put(key, value);
                skipWs();
                char n = next();
                if (n == '}') {
                    return map;
                }
                if (n != ',') {
                    throw new JsonException("对象期望 ',' 或 '}'，位置 " + pos);
                }
            }
        }

        private List<Object> readArray() {
            List<Object> list = new ArrayList<>();
            expect('[');
            skipWs();
            if (peek() == ']') {
                pos++;
                return list;
            }
            while (true) {
                Object value = readValue();
                list.add(value);
                skipWs();
                char n = next();
                if (n == ']') {
                    return list;
                }
                if (n != ',') {
                    throw new JsonException("数组期望 ',' 或 ']'，位置 " + pos);
                }
            }
        }

        private String readString() {
            expect('"');
            StringBuilder sb = new StringBuilder();
            while (true) {
                if (eof()) {
                    throw new JsonException("字符串未闭合");
                }
                char c = s.charAt(pos++);
                if (c == '"') {
                    return sb.toString();
                }
                if (c == '\\') {
                    char esc = s.charAt(pos++);
                    switch (esc) {
                        case '"':
                            sb.append('"');
                            break;
                        case '\\':
                            sb.append('\\');
                            break;
                        case '/':
                            sb.append('/');
                            break;
                        case 'n':
                            sb.append('\n');
                            break;
                        case 'r':
                            sb.append('\r');
                            break;
                        case 't':
                            sb.append('\t');
                            break;
                        case 'b':
                            sb.append('\b');
                            break;
                        case 'f':
                            sb.append('\f');
                            break;
                        case 'u':
                            String hex = s.substring(pos, pos + 4);
                            pos += 4;
                            sb.append((char) Integer.parseInt(hex, 16));
                            break;
                        default:
                            throw new JsonException("非法转义 \\" + esc);
                    }
                } else {
                    sb.append(c);
                }
            }
        }

        private Object readNumber() {
            int start = pos;
            boolean isDouble = false;
            if (peek() == '-') {
                pos++;
            }
            while (!eof()) {
                char c = s.charAt(pos);
                if (c >= '0' && c <= '9') {
                    pos++;
                } else if (c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
                    isDouble = true;
                    pos++;
                } else {
                    break;
                }
            }
            String num = s.substring(start, pos);
            if (num.isEmpty() || "-".equals(num)) {
                throw new JsonException("非法数字，位置 " + start);
            }
            if (isDouble) {
                return Double.parseDouble(num);
            }
            try {
                return Long.parseLong(num);
            } catch (NumberFormatException e) {
                return Double.parseDouble(num);
            }
        }

        private Boolean readBoolean() {
            if (s.startsWith("true", pos)) {
                pos += 4;
                return Boolean.TRUE;
            }
            if (s.startsWith("false", pos)) {
                pos += 5;
                return Boolean.FALSE;
            }
            throw new JsonException("非法布尔值，位置 " + pos);
        }

        private Object readNull() {
            if (s.startsWith("null", pos)) {
                pos += 4;
                return null;
            }
            throw new JsonException("非法 null，位置 " + pos);
        }

        private char peek() {
            if (eof()) {
                throw new JsonException("意外的输入结束");
            }
            return s.charAt(pos);
        }

        private char next() {
            if (eof()) {
                throw new JsonException("意外的输入结束");
            }
            return s.charAt(pos++);
        }

        private void expect(char c) {
            char actual = next();
            if (actual != c) {
                throw new JsonException("期望 '" + c + "' 实际 '" + actual + "'，位置 " + (pos - 1));
            }
        }
    }
}
