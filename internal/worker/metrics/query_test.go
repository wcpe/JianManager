package metrics

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildQueryDatagram 构造一个 Query 响应数据报：type + sessionID + body。
func buildQueryDatagram(typ byte, body []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(typ)
	var sid [4]byte
	binary.BigEndian.PutUint32(sid[:], querySessionID)
	b.Write(sid[:])
	b.Write(body)
	return b.Bytes()
}

// buildFullStatSingle 构造单包（未分片）Full Stat 负载：splitnum 头(0x80) + KV + 玩家段。
func buildFullStatSingle(kv []byte, players []string) []byte {
	var b bytes.Buffer
	b.Write(querySplitPrefix) // "splitnum\0"
	b.WriteByte(0x80)         // 未分片标记
	b.WriteByte(0x00)
	b.Write(kv) // 已含末尾空 key
	b.Write(queryPlayerMarker)
	for _, p := range players {
		b.WriteString(p)
		b.WriteByte(0x00)
	}
	b.WriteByte(0x00)
	return b.Bytes()
}

// buildFullStatFragment 构造一个分片：splitnum 头(count,index) + content。
func buildFullStatFragment(count, index byte, content []byte) []byte {
	var b bytes.Buffer
	b.Write(querySplitPrefix)
	b.WriteByte(count)
	b.WriteByte(index)
	b.Write(content)
	return b.Bytes()
}

// sampleKV 构造 Full Stat 的 KV 段（NUL 分隔 + 末尾空 key）。
func sampleKV() []byte {
	var b bytes.Buffer
	pairs := [][2]string{
		{"hostname", "§aReal Server"},
		{"gametype", "SMP"},
		{"version", "1.20.4"},
		{"plugins", "Paper on 1.20.4; WorldEdit 7.2; EssentialsX"},
		{"map", "world_nether"},
		{"numplayers", "2"},
		{"maxplayers", "50"},
		{"hostport", "25565"},
		{"hostip", "127.0.0.1"},
	}
	for _, p := range pairs {
		b.WriteString(p[0])
		b.WriteByte(0x00)
		b.WriteString(p[1])
		b.WriteByte(0x00)
	}
	b.WriteByte(0x00) // 空 key 结束 KV 段
	return b.Bytes()
}

// TestParseQueryHandshakeResponse 覆盖握手响应解析（含超过 int32 的 token）。
func TestParseQueryHandshakeResponse(t *testing.T) {
	tok, err := parseQueryHandshakeResponse(buildQueryDatagram(queryTypeHandshake, []byte("9513307\x00")))
	require.NoError(t, err)
	assert.Equal(t, uint32(9513307), tok)

	// 大 token（>2^31）也要能解析。
	tok, err = parseQueryHandshakeResponse(buildQueryDatagram(queryTypeHandshake, []byte("3616368527\x00")))
	require.NoError(t, err)
	assert.Equal(t, uint32(3616368527), tok)

	// 类型不符 → 报错。
	_, err = parseQueryHandshakeResponse(buildQueryDatagram(queryTypeStat, []byte("1\x00")))
	assert.Error(t, err)
}

// TestBuildQueryRequests 断言请求字节。
func TestBuildQueryRequests(t *testing.T) {
	assert.Equal(t, []byte{0xFE, 0xFD, 0x09, 0x00, 0x00, 0x00, 0x01}, buildQueryHandshake())
	got := buildQueryFullStat(0x0091295B)
	want := []byte{0xFE, 0xFD, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x91, 0x29, 0x5B, 0x00, 0x00, 0x00, 0x00}
	assert.Equal(t, want, got)
}

// TestDecodeQueryFullStatSingle 覆盖单包负载解析（KV + 玩家名单 + MOTD 去色 + 插件切分）。
func TestDecodeQueryFullStatSingle(t *testing.T) {
	payload := buildFullStatSingle(sampleKV(), []string{"barneygale", "Vivalahelvig"})
	kv, players, err := decodeQueryFullStatPayload(payload)
	require.NoError(t, err)
	assert.Equal(t, "§aReal Server", kv["hostname"])
	assert.Equal(t, "1.20.4", kv["version"])
	assert.Equal(t, "world_nether", kv["map"])
	assert.Equal(t, []string{"barneygale", "Vivalahelvig"}, players)

	snap := buildQuerySnapshot(kv, players)
	assert.Equal(t, "Real Server", snap.Motd)
	assert.Equal(t, "1.20.4", snap.Version)
	assert.Equal(t, "world_nether", snap.Map)
	assert.Equal(t, int32(2), snap.PlayersOnline)
	assert.Equal(t, int32(50), snap.PlayersMax)
	assert.Equal(t, 25565, snap.HostPort)
	assert.Equal(t, []string{"barneygale", "Vivalahelvig"}, snap.PlayerNames)
	assert.Equal(t, []string{"Paper on 1.20.4", "WorldEdit 7.2", "EssentialsX"}, snap.Plugins)
}

// TestReassembleQueryFullStatSplit 覆盖跨 UDP 包分片拼接（2 片）。
func TestReassembleQueryFullStatSplit(t *testing.T) {
	full := buildFullStatSingle(sampleKV(), []string{"alpha", "beta"})
	// 在固定头之后的负载处切分成两片。
	content := full
	mid := len(content) / 2
	datagrams := [][]byte{
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(2, 0, content[:mid])),
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(2, 1, content[mid:])),
	}
	kv, players, err := parseQueryFullStat(datagrams)
	require.NoError(t, err)
	assert.Equal(t, "SMP", kv["gametype"])
	assert.Equal(t, []string{"alpha", "beta"}, players)

	// 顺序颠倒也应正确拼接（按 index 排序，而非到达顺序）。
	reversed := [][]byte{datagrams[1], datagrams[0]}
	kv2, players2, err := parseQueryFullStat(reversed)
	require.NoError(t, err)
	assert.Equal(t, kv, kv2)
	assert.Equal(t, players, players2)
}

// TestReassembleQueryFullStatMissingFragment 分片缺失应报错而非返回残缺数据。
func TestReassembleQueryFullStatMissingFragment(t *testing.T) {
	datagrams := [][]byte{
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(3, 0, []byte("a\x00"))),
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(3, 2, []byte("b\x00"))),
	}
	_, _, err := parseQueryFullStat(datagrams)
	assert.Error(t, err)
}

// TestQueryResponseComplete 判断单包/分片是否收齐。
func TestQueryResponseComplete(t *testing.T) {
	single := [][]byte{buildQueryDatagram(queryTypeStat, buildFullStatSingle(sampleKV(), nil))}
	assert.True(t, queryResponseComplete(single))

	partial := [][]byte{buildQueryDatagram(queryTypeStat, buildFullStatFragment(2, 0, []byte("x")))}
	assert.False(t, queryResponseComplete(partial))

	complete := [][]byte{
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(2, 0, []byte("x"))),
		buildQueryDatagram(queryTypeStat, buildFullStatFragment(2, 1, []byte("y"))),
	}
	assert.True(t, queryResponseComplete(complete))
}

// TestSplitQueryPlugins 覆盖插件串切分。
func TestSplitQueryPlugins(t *testing.T) {
	assert.Equal(t, []string{"Paper on 1.20.4"}, splitQueryPlugins("Paper on 1.20.4"))
	assert.Equal(t, []string{"A", "B", "C"}, splitQueryPlugins("A;B, C"))
	assert.Nil(t, splitQueryPlugins(""))
}

// TestDecodeQueryFullStatEmptyPlayers 无玩家时玩家段为空（不报错）。
func TestDecodeQueryFullStatEmptyPlayers(t *testing.T) {
	payload := buildFullStatSingle(sampleKV(), nil)
	_, players, err := decodeQueryFullStatPayload(payload)
	require.NoError(t, err)
	assert.Empty(t, players)
}

// TestQueryServerAgainstFakeUDPServer 用假 UDP 服务端验证握手 + Full Stat 全链路。
func TestQueryServerAgainstFakeUDPServer(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer pc.Close()

	fullStat := buildQueryDatagram(queryTypeStat, buildFullStatSingle(sampleKV(), []string{"Notch"}))
	handshakeResp := buildQueryDatagram(queryTypeHandshake, []byte("12345\x00"))

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n >= 3 && buf[0] == 0xFE && buf[1] == 0xFD && buf[2] == queryTypeHandshake {
				_, _ = pc.WriteToUDP(handshakeResp, addr)
				continue
			}
			if n >= 3 && buf[0] == 0xFE && buf[1] == 0xFD && buf[2] == queryTypeStat {
				_, _ = pc.WriteToUDP(fullStat, addr)
			}
		}
	}()

	port := pc.LocalAddr().(*net.UDPAddr).Port
	snap, err := QueryServer("127.0.0.1", port, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "Real Server", snap.Motd)
	assert.Equal(t, []string{"Notch"}, snap.PlayerNames)
	assert.Equal(t, int32(50), snap.PlayersMax)
}

// TestQueryServerUnavailable 未配置端口 / UDP 无响应 → 返回 error（降级用）。
func TestQueryServerUnavailable(t *testing.T) {
	_, err := QueryServer("127.0.0.1", 0, time.Second)
	assert.Error(t, err)

	// 随机端口上的 UDP 通常无响应 → 超时错误。
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	port := pc.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, pc.Close())
	_, err = QueryServer("127.0.0.1", port, 300*time.Millisecond)
	assert.Error(t, err)
}
