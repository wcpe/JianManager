package metrics

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// SLPSnapshot 是 Minecraft Server List Ping（SLP）状态响应的解析结果（FR-446）。
//
// SLP 是 MC 协议内建能力：任何在跑的 Java 版服务端（含 BungeeCord/Velocity 代理）
// 无需任何配置即支持，是「保底可见」的关键来源。它取 MOTD / 版本 / 在线人数 /
// 最大人数 / favicon；players.sample 弱信息仅作「可能在线」参考（可被插件伪造/截断），
// 实名名单只认 Query（见 ADR-092）。
type SLPSnapshot struct {
	Motd          string   // description 扁平化 + 剥离颜色码后的纯文本
	Version       string   // version.name，如 "1.20.4"
	Protocol      int      // version.protocol
	PlayersOnline int32    // players.online
	PlayersMax    int32    // players.max
	PlayerSample  []string // players.sample 的玩家名（弱信息，不保证返回）
	Favicon       string   // data:image/png;base64,...（可空）
}

const (
	// slpDefaultPort 是 SLP 默认端口（server-port）。
	slpDefaultPort = 25565
	// slpStatusState 是握手 nextState=1（status ping）。
	slpStatusState = 1
	// slpProtocolProbe 是探测用协议版本 -1（按 MC 约定表示「我只要状态」）。
	slpProtocolProbe = -1
	// maxSLPResponseBytes 是 SLP 响应长度上限，防超大响应内存放大。
	maxSLPResponseBytes = 1 << 20 // 1MiB
)

// PingSLP 面向 server-port 做一次 SLP 状态探测并解析结果（FR-446）。
// 超时/连接拒绝/协议不符 → 返回 error，调用方据此降级（不 panic、不阻塞其它来源）。
func PingSLP(host string, port int, timeout time.Duration) (*SLPSnapshot, error) {
	if port <= 0 {
		port = slpDefaultPort
	}
	if timeout <= 0 {
		timeout = defaultDirectProbeTimeout
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("SLP 连接 %s 失败: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write(buildSLPHandshake(host, port)); err != nil {
		return nil, fmt.Errorf("SLP 发送握手失败: %w", err)
	}
	if _, err := conn.Write(buildSLPStatusRequest()); err != nil {
		return nil, fmt.Errorf("SLP 发送状态请求失败: %w", err)
	}
	return readSLPStatusResponse(conn)
}

// buildSLPHandshake 构造 SLP 握手包（含长度前缀）。
// 格式：packetId(0x00) + protocolVersion(VarInt=-1) + serverAddr(String) + serverPort(uint16 大端) + nextState(VarInt=1)。
func buildSLPHandshake(host string, port int) []byte {
	body := &bytes.Buffer{}
	writeVarInt(body, 0x00)
	writeVarInt(body, int32(slpProtocolProbe))
	writeString(body, host)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	body.Write(p[:])
	writeVarInt(body, slpStatusState)
	return framePacket(body.Bytes())
}

// buildSLPStatusRequest 构造状态请求包（packetId 0x00，无 payload）。
func buildSLPStatusRequest() []byte {
	body := &bytes.Buffer{}
	writeVarInt(body, 0x00)
	return framePacket(body.Bytes())
}

// framePacket 给包体加 VarInt 长度前缀。
func framePacket(body []byte) []byte {
	out := &bytes.Buffer{}
	writeVarInt(out, int32(len(body)))
	out.Write(body)
	return out.Bytes()
}

// readSLPStatusResponse 读取 `[length:VarInt][packetId][JSON String]` 并解析。
func readSLPStatusResponse(r io.Reader) (*SLPSnapshot, error) {
	br := bufio.NewReader(r)
	length, err := readVarInt(br)
	if err != nil {
		return nil, fmt.Errorf("SLP 读取响应长度失败: %w", err)
	}
	if length < 0 || int(length) > maxSLPResponseBytes {
		return nil, fmt.Errorf("SLP 响应长度非法: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, fmt.Errorf("SLP 读取响应体失败: %w", err)
	}
	return parseSLPStatusPacket(payload)
}

// parseSLPStatusPacket 解析状态响应包体（不含长度前缀）：packetId + JSON String。
func parseSLPStatusPacket(payload []byte) (*SLPSnapshot, error) {
	r := bytes.NewReader(payload)
	packetID, err := readVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("SLP 读取 packetId 失败: %w", err)
	}
	if packetID != 0x00 {
		return nil, fmt.Errorf("SLP 非预期 packetId: 0x%02X", packetID)
	}
	jsonLen, err := readVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("SLP 读取 JSON 长度失败: %w", err)
	}
	if jsonLen < 0 || int(jsonLen) > r.Len() {
		return nil, fmt.Errorf("SLP JSON 长度非法: %d（剩余 %d 字节）", jsonLen, r.Len())
	}
	raw := make([]byte, jsonLen)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, fmt.Errorf("SLP 读取 JSON 失败: %w", err)
	}
	return parseSLPStatusJSON(raw)
}

// slpStatusResponse 映射 SLP 状态响应的 JSON 结构（只取关心字段）。
type slpStatusResponse struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
		Sample []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"sample"`
	} `json:"players"`
	Description json.RawMessage `json:"description"`
	Favicon     string          `json:"favicon"`
}

// parseSLPStatusJSON 解析 SLP 状态响应 JSON。纯函数、无 IO，便于用假响应字节穷举测试。
func parseSLPStatusJSON(raw []byte) (*SLPSnapshot, error) {
	var resp slpStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("SLP 状态 JSON 解析失败: %w", err)
	}
	snap := &SLPSnapshot{
		Motd:          stripColorCodes(flattenChat(resp.Description)),
		Version:       resp.Version.Name,
		Protocol:      resp.Version.Protocol,
		PlayersOnline: int32(resp.Players.Online),
		PlayersMax:    int32(resp.Players.Max),
		Favicon:       resp.Favicon,
	}
	for _, s := range resp.Players.Sample {
		if s.Name != "" {
			snap.PlayerSample = append(snap.PlayerSample, s.Name)
		}
	}
	return snap, nil
}

// flattenChat 把 description 字段扁平化为纯文本。
// description 可能是字符串（pre-1.19 风格）或 chat 组件对象/数组（{"text":...} / {"extra":[...]}）；
// Notchian 服内嵌 § 颜色码，第三方服（Spigot/Paper）返回完整组件，两种都需处理。
func flattenChat(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	case '{':
		var comp struct {
			Text  string            `json:"text"`
			Extra []json.RawMessage `json:"extra"`
		}
		if err := json.Unmarshal(raw, &comp); err != nil {
			return ""
		}
		var b strings.Builder
		b.WriteString(comp.Text)
		for _, e := range comp.Extra {
			b.WriteString(flattenChat(e))
		}
		return b.String()
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return ""
		}
		var b strings.Builder
		for _, e := range arr {
			b.WriteString(flattenChat(e))
		}
		return b.String()
	}
	return ""
}

// stripColorCodes 剥离 Minecraft 传统颜色/格式码（§ 后跟一个字符，如 §a§l）。
// SLP（内嵌 §）与 Query（MOTD 可能含 §）共用。
func stripColorCodes(s string) string {
	if !strings.ContainsRune(s, '§') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	skip := false
	for _, r := range s {
		if skip {
			skip = false
			continue
		}
		if r == '§' {
			skip = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeVarInt 把 n 按 Minecraft VarInt 编码写入 buf：低 7 位一组、LSB 先、续位 0x80。
func writeVarInt(buf *bytes.Buffer, n int32) {
	u := uint32(n)
	for {
		if u&^0x7F == 0 {
			buf.WriteByte(byte(u))
			return
		}
		buf.WriteByte(byte(u&0x7F | 0x80))
		u >>= 7
	}
}

// readVarInt 读一个 VarInt，最多 5 字节（超过视为协议错误）。
func readVarInt(r io.ByteReader) (int32, error) {
	var result uint32
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return int32(result), nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("VarInt 超过 5 字节")
}

// writeString 写入 `VarInt 长度前缀 + UTF-8 字节`。
func writeString(buf *bytes.Buffer, s string) {
	writeVarInt(buf, int32(len(s)))
	buf.WriteString(s)
}
