package metrics

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// QuerySnapshot 是 Minecraft Query（GameSpy4/UT3）Full Stat 的解析结果（FR-446）。
//
// Query 面向 query.port 的 UDP，需 enable-query=true；它提供 SLP 给不了的
// **实名玩家名单**、插件列表与地图名，是玩家名单的唯一可信来源（见 ADR-092）。
// 前置条件不满足（未开 query / 端口封闭）时表现为 UDP 无响应，属「不可用」而非错误。
type QuerySnapshot struct {
	Motd          string
	Version       string
	Plugins       []string
	Map           string
	PlayersOnline int32
	PlayersMax    int32
	PlayerNames   []string // 实名玩家名单（唯一可信来源）
	HostPort      int
}

const (
	queryMagic0 = 0xFE
	queryMagic1 = 0xFD
	// queryTypeHandshake 是握手请求/响应的类型字节。
	queryTypeHandshake = 0x09
	// queryTypeStat 是 Basic/Full Stat 请求/响应的类型字节。
	queryTypeStat = 0x00
	// queryMaxResponseBytes 是 Query 响应（含分片拼接）上限，防内存放大。
	queryMaxResponseBytes = 64 << 10 // 64KiB
	// querySplitHeaderLen 是 Full Stat 负载前的固定头长度："splitnum\0" + 分片计数 + 分片索引。
	querySplitHeaderLen = 11
	// querySessionID 是会话 ID；MC 只用低 4 位且现代版本不校验，固定值即可。
	querySessionID = 0x01
)

// querySplitPrefix 是 Full Stat 负载固定头的可读部分（后接 1 字节计数 + 1 字节索引）。
var querySplitPrefix = []byte("splitnum\x00")

// queryPlayerMarker 是 KV 段与玩家名单段之间的固定分隔："\x01player_\0\0"。
var queryPlayerMarker = []byte("\x01player_\x00\x00")

// QueryServer 面向 query.port 做一次 Query Full Stat 探测并解析结果（FR-446）。
// UDP 无响应/超时/协议不符 → 返回 error，调用方据此降级（不 panic、不阻塞其它来源）。
func QueryServer(host string, port int, timeout time.Duration) (*QuerySnapshot, error) {
	if port <= 0 {
		return nil, fmt.Errorf("Query 端口未配置")
	}
	if timeout <= 0 {
		timeout = defaultDirectProbeTimeout
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("Query 连接 %s 失败: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// 1) 握手取 challenge token（响应为 type(0x09) + sessionID(4) + ASCII 十进制 token + NUL）。
	if _, err := conn.Write(buildQueryHandshake()); err != nil {
		return nil, fmt.Errorf("Query 发送握手失败: %w", err)
	}
	handshakeResp, err := readQueryDatagram(conn)
	if err != nil {
		return nil, fmt.Errorf("Query 握手无响应（enable-query 未开或端口不可达）: %w", err)
	}
	token, err := parseQueryHandshakeResponse(handshakeResp)
	if err != nil {
		return nil, err
	}

	// 2) Full Stat（响应可能跨多个 UDP 包，按分片头拼接）。
	if _, err := conn.Write(buildQueryFullStat(token)); err != nil {
		return nil, fmt.Errorf("Query 发送 Full Stat 失败: %w", err)
	}
	datagrams, err := readQueryFullStatDatagrams(conn)
	if err != nil {
		return nil, err
	}
	kv, players, err := parseQueryFullStat(datagrams)
	if err != nil {
		return nil, err
	}
	return buildQuerySnapshot(kv, players), nil
}

// buildQueryHandshake 构造握手请求：Magic(FE FD) + type(0x09) + sessionID(int32 大端)。
func buildQueryHandshake() []byte {
	return buildQueryRequest(queryTypeHandshake, nil)
}

// buildQueryFullStat 构造 Full Stat 请求：Magic + type(0x00) + sessionID + token(int32 大端) + 4 字节补白。
// 规范要求 Full Stat 的负载补齐到 8 字节。
func buildQueryFullStat(token uint32) []byte {
	var tok [4]byte
	binary.BigEndian.PutUint32(tok[:], token)
	return buildQueryRequest(queryTypeStat, append(tok[:], 0, 0, 0, 0))
}

// buildQueryRequest 拼装请求：Magic 两字节 + 类型 + 会话 ID（4 字节大端）+ 负载。
func buildQueryRequest(typ byte, payload []byte) []byte {
	b := make([]byte, 0, 7+len(payload))
	b = append(b, queryMagic0, queryMagic1, typ)
	var sid [4]byte
	binary.BigEndian.PutUint32(sid[:], querySessionID)
	b = append(b, sid[:]...)
	b = append(b, payload...)
	return b
}

// parseQueryHandshakeResponse 解析握手响应，返回 challenge token（4 字节大端的无符号值）。
// 纯函数、无 IO，便于用假响应字节测试。
func parseQueryHandshakeResponse(data []byte) (uint32, error) {
	if len(data) < 5 {
		return 0, fmt.Errorf("Query 握手响应过短: %d 字节", len(data))
	}
	if data[0] != queryTypeHandshake {
		return 0, fmt.Errorf("Query 非预期握手类型: 0x%02X", data[0])
	}
	tokenStr := strings.TrimSpace(strings.TrimRight(string(data[5:]), "\x00"))
	if tokenStr == "" {
		return 0, fmt.Errorf("Query 握手响应缺 challenge token")
	}
	v, err := strconv.ParseUint(tokenStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("Query challenge token 解析失败 %q: %w", tokenStr, err)
	}
	return uint32(v), nil
}

// readQueryDatagram 读一个 UDP 数据报（单包上限 2048 字节，Query 单包不会更大）。
func readQueryDatagram(conn net.Conn) ([]byte, error) {
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("空 UDP 响应")
	}
	return buf[:n], nil
}

// readQueryFullStatDatagrams 读取 Full Stat 响应；分片时按分片头继续收下一片直到完整、读到超时或超上限。
// 已有分片后再读超时按「分片收齐」处理（UDP 无流结束标记，best-effort）。
func readQueryFullStatDatagrams(conn net.Conn) ([][]byte, error) {
	var out [][]byte
	total := 0
	for {
		d, err := readQueryDatagram(conn)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, fmt.Errorf("Query Full Stat 无响应: %w", err)
		}
		total += len(d)
		if total > queryMaxResponseBytes {
			return nil, fmt.Errorf("Query 响应超过上限 %d 字节", queryMaxResponseBytes)
		}
		out = append(out, d)
		if queryResponseComplete(out) {
			return out, nil
		}
	}
}

// queryResponseComplete 判断已收到的数据报是否构成一份完整 Full Stat 响应。
// 单包（分片计数高位 0x80）或无分片头即完整；分片则需 0..count-1 全部到齐。
func queryResponseComplete(datagrams [][]byte) bool {
	total := -1
	got := map[int]bool{}
	for _, d := range datagrams {
		if len(d) < 5+querySplitHeaderLen {
			return true // 无分片头 → 视为单包完整
		}
		body := d[5:]
		if !bytes.HasPrefix(body, querySplitPrefix) {
			return true
		}
		count := int(body[9])
		index := int(body[10])
		if count&0x80 != 0 {
			return true // 0x80 = 未分片
		}
		total = count
		got[index] = true
	}
	if total <= 0 {
		return true
	}
	for i := 0; i < total; i++ {
		if !got[i] {
			return false
		}
	}
	return true
}

// parseQueryFullStat 解析（可能多片拼接的）Full Stat 响应，返回键值对与实名玩家名单。
// 纯函数、无 IO，便于用假响应字节覆盖分片拼接与名单解析。
func parseQueryFullStat(datagrams [][]byte) (map[string]string, []string, error) {
	payload, err := reassembleQueryFullStat(datagrams)
	if err != nil {
		return nil, nil, err
	}
	return decodeQueryFullStatPayload(payload)
}

// reassembleQueryFullStat 校验各数据报并按分片索引拼成完整负载（逐片剥离固定头）。
// 每片：type(0x00) + sessionID(4) + [splitnum\0][count][index] + content。
// count 高位 0x80 = 未分片（单包）；否则 count 为总片数、index 为片序。
func reassembleQueryFullStat(datagrams [][]byte) ([]byte, error) {
	type frag struct {
		index   int
		content []byte
	}
	var frags []frag
	var single []byte
	for _, d := range datagrams {
		if len(d) < 5 || d[0] != queryTypeStat {
			continue
		}
		body := d[5:]
		if len(body) >= querySplitHeaderLen && bytes.HasPrefix(body, querySplitPrefix) {
			count := int(body[9])
			index := int(body[10])
			content := body[querySplitHeaderLen:]
			if count&0x80 != 0 {
				return content, nil // 单包完整响应
			}
			frags = append(frags, frag{index: index, content: content})
			continue
		}
		// 无固定头：容忍性回退，直接作为负载拼接。
		single = append(single, body...)
	}
	if len(frags) == 0 {
		return single, nil
	}
	maxIdx := -1
	for _, f := range frags {
		if f.index > maxIdx {
			maxIdx = f.index
		}
	}
	chunks := make([][]byte, maxIdx+1)
	seen := make([]bool, maxIdx+1)
	for _, f := range frags {
		if f.index < 0 || f.index >= len(chunks) || seen[f.index] {
			continue
		}
		seen[f.index] = true
		chunks[f.index] = f.content
	}
	var buf bytes.Buffer
	for i := range chunks {
		if !seen[i] {
			return nil, fmt.Errorf("Query Full Stat 分片缺失: 索引 %d", i)
		}
		buf.Write(chunks[i])
	}
	return buf.Bytes(), nil
}

// decodeQueryFullStatPayload 解析 Full Stat 负载：
// 可选的 "splitnum\0" 头 → NUL 分隔的 key/value 对（空 key 结束）→ "\x01player_\0\0" 标记 → NUL 分隔的玩家名（空名结束）。
func decodeQueryFullStatPayload(payload []byte) (map[string]string, []string, error) {
	// 容忍负载前仍残留 "splitnum\0"+2 字节固定头的情形。
	if bytes.HasPrefix(payload, querySplitPrefix) && len(payload) >= querySplitHeaderLen {
		payload = payload[querySplitHeaderLen:]
	}
	kv := map[string]string{}
	rest := payload
	for {
		key, tail, ok := readCString(rest)
		if !ok {
			return nil, nil, fmt.Errorf("Query KV 段缺少终止符")
		}
		if key == "" {
			rest = tail
			break
		}
		value, tail2, ok := readCString(tail)
		if !ok {
			return nil, nil, fmt.Errorf("Query KV 对 %q 缺少值终止符", key)
		}
		kv[key] = value
		rest = tail2
	}
	var players []string
	if idx := bytes.Index(rest, queryPlayerMarker); idx >= 0 {
		rest = rest[idx+len(queryPlayerMarker):]
		for {
			name, tail, ok := readCString(rest)
			if !ok || name == "" {
				break
			}
			players = append(players, name)
			rest = tail
		}
	}
	return kv, players, nil
}

// readCString 读取以 NUL 结尾的字符串，返回内容与剩余字节；无 NUL 时 ok=false。
func readCString(b []byte) (string, []byte, bool) {
	i := bytes.IndexByte(b, 0x00)
	if i < 0 {
		return "", nil, false
	}
	return string(b[:i]), b[i+1:], true
}

// buildQuerySnapshot 把 Full Stat 的键值对与玩家名单归一到 QuerySnapshot。
func buildQuerySnapshot(kv map[string]string, players []string) *QuerySnapshot {
	snap := &QuerySnapshot{
		Motd:        stripColorCodes(kv["hostname"]),
		Version:     kv["version"],
		Map:         kv["map"],
		PlayerNames: players,
	}
	if v, err := strconv.Atoi(strings.TrimSpace(kv["numplayers"])); err == nil {
		snap.PlayersOnline = int32(v)
	}
	if v, err := strconv.Atoi(strings.TrimSpace(kv["maxplayers"])); err == nil {
		snap.PlayersMax = int32(v)
	}
	if v, err := strconv.Atoi(strings.TrimSpace(kv["hostport"])); err == nil {
		snap.HostPort = v
	}
	snap.Plugins = splitQueryPlugins(kv["plugins"])
	return snap
}

// splitQueryPlugins 把 Query 的 plugins 字段拆成插件名列表。
// 原始格式形如 "CraftBukkit on Bukkit 1.2.5-R4.0: WorldEdit 5.3; CommandBook 2.1" 或 "Paper on 1.20.4"，
// 以 ";" 或 "," 分隔；空串（vanilla）返回 nil。首段可能是服务端软件描述，一并保留。
func splitQueryPlugins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
