package metrics

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVarIntRoundTrip 覆盖 Minecraft VarInt 编解码往返（含 -1 的 5 字节编码）。
func TestVarIntRoundTrip(t *testing.T) {
	cases := []int32{0, 1, 127, 128, 255, 2147483647, -1, -2147483648}
	for _, want := range cases {
		var buf bytes.Buffer
		writeVarInt(&buf, want)
		got, err := readVarInt(bytes.NewReader(buf.Bytes()))
		require.NoError(t, err, "值 %d", want)
		assert.Equal(t, want, got)
	}
}

// TestWriteVarIntKnownBytes 断言协议约定的 VarInt 字节（-1 必须是 FF FF FF FF 0F）。
func TestWriteVarIntKnownBytes(t *testing.T) {
	var buf bytes.Buffer
	writeVarInt(&buf, -1)
	assert.Equal(t, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x0F}, buf.Bytes())

	buf.Reset()
	writeVarInt(&buf, 255)
	assert.Equal(t, []byte{0xFF, 0x01}, buf.Bytes())
}

// TestReadVarIntTooLong 拒绝超过 5 字节的 VarInt。
func TestReadVarIntTooLong(t *testing.T) {
	_, err := readVarInt(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}))
	assert.Error(t, err)
}

// TestBuildSLPHandshake 断言握手包完整字节（localhost:25565 状态探测）。
func TestBuildSLPHandshake(t *testing.T) {
	got := buildSLPHandshake("localhost", 25565)
	want := []byte{
		0x13,                         // 长度前缀 = 19
		0x00,                         // packetId
		0xFF, 0xFF, 0xFF, 0xFF, 0x0F, // protocolVersion = -1
		0x09, 'l', 'o', 'c', 'a', 'l', 'h', 'o', 's', 't', // serverAddress
		0x63, 0xDD, // serverPort 25565 大端
		0x01, // nextState=1
	}
	assert.Equal(t, want, got)
	assert.Equal(t, []byte{0x01, 0x00}, buildSLPStatusRequest())
}

// TestParseSLPStatusJSONStringDescription 覆盖 description 为纯字符串的情形。
func TestParseSLPStatusJSONStringDescription(t *testing.T) {
	raw := []byte(`{"version":{"name":"1.20.4","protocol":765},` +
		`"players":{"max":20,"online":5,"sample":[{"name":"Steve","id":"uuid-1"}]},` +
		`"description":"A Minecraft Server","favicon":"data:image/png;base64,AAAA"}`)
	snap, err := parseSLPStatusJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, "A Minecraft Server", snap.Motd)
	assert.Equal(t, "1.20.4", snap.Version)
	assert.Equal(t, 765, snap.Protocol)
	assert.Equal(t, int32(5), snap.PlayersOnline)
	assert.Equal(t, int32(20), snap.PlayersMax)
	assert.Equal(t, []string{"Steve"}, snap.PlayerSample)
	assert.Equal(t, "data:image/png;base64,AAAA", snap.Favicon)
}

// TestParseSLPStatusJSONComponentDescription 覆盖 description 为 chat 组件（含 extra 数组）与 § 颜色码。
func TestParseSLPStatusJSONComponentDescription(t *testing.T) {
	raw := []byte(`{"version":{"name":"Paper"},"players":{"max":100,"online":3},` +
		`"description":{"text":"§aHello ","extra":[{"text":"world"},{"extra":[{"text":"!"}]}]}}`)
	snap, err := parseSLPStatusJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, "Hello world!", snap.Motd, "组件需扁平化且剥离颜色码")
	assert.Equal(t, int32(3), snap.PlayersOnline)
	assert.Equal(t, int32(100), snap.PlayersMax)
	assert.Empty(t, snap.PlayerSample)
}

// TestParseSLPStatusPacket 覆盖 `packetId + JSON String` 包体解析。
func TestParseSLPStatusPacket(t *testing.T) {
	j := []byte(`{"version":{"name":"1.21"},"players":{"max":10,"online":0},"description":"hi"}`)
	var body bytes.Buffer
	writeVarInt(&body, 0x00)
	writeVarInt(&body, int32(len(j)))
	body.Write(j)

	snap, err := parseSLPStatusPacket(body.Bytes())
	require.NoError(t, err)
	assert.Equal(t, "hi", snap.Motd)
	assert.Equal(t, "1.21", snap.Version)

	// 非 0x00 packetId 应报错。
	_, err = parseSLPStatusPacket([]byte{0x01, 0x02})
	assert.Error(t, err)
}

// TestStripColorCodes 覆盖 § 颜色码剥离（含连续码与结尾码）。
func TestStripColorCodes(t *testing.T) {
	assert.Equal(t, "Hello", stripColorCodes("§a§lHello"))
	assert.Equal(t, "AB", stripColorCodes("A§rB"))
	assert.Equal(t, "trailing", stripColorCodes("trailing§"))
	assert.Equal(t, "plain", stripColorCodes("plain"))
}

// TestPingSLPAgainstFakeServer 用假 TCP 服务端（返回固定响应字节）验证握手/请求/解析全链路。
func TestPingSLPAgainstFakeServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	j := []byte(`{"version":{"name":"1.20.4","protocol":765},"players":{"max":20,"online":2,"sample":[{"name":"Alex"}]},"description":"Fake MOTD","favicon":"data:image/png;base64,BBBB"}`)
	var resp bytes.Buffer
	writeVarInt(&resp, int32(1+jsonVarIntLen(j)+len(j))) // packetId + String
	writeVarInt(&resp, 0x00)
	writeVarInt(&resp, int32(len(j)))
	resp.Write(j)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf) // 读握手（客户端随后发的状态请求留在接收缓冲）
		_, _ = conn.Write(resp.Bytes())
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	snap, err := PingSLP("127.0.0.1", port, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "Fake MOTD", snap.Motd)
	assert.Equal(t, int32(2), snap.PlayersOnline)
	assert.Equal(t, []string{"Alex"}, snap.PlayerSample)
}

// TestPingSLPTimeout 覆盖连接超时 → 返回 error（用于降级，不 panic）。
func TestPingSLPTimeout(t *testing.T) {
	// 关闭的端口，连接应快速失败。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	_, err = PingSLP("127.0.0.1", port, 500*time.Millisecond)
	assert.Error(t, err)
}

// jsonVarIntLen 返回 JSON 长度的 VarInt 编码字节数（测试构造响应体用）。
func jsonVarIntLen(b []byte) int {
	var buf bytes.Buffer
	writeVarInt(&buf, int32(len(b)))
	return buf.Len()
}
