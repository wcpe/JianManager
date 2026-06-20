package metrics

import (
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rconPacket 解出一个 RCON 帧（小端：len 不含自身；body = reqID(4)+type(4)+payload+2 空字节）。
func readRCONFrame(t *testing.T, conn net.Conn) (reqID, typ int32, payload string) {
	t.Helper()
	var ln int32
	require.NoError(t, binary.Read(conn, binary.LittleEndian, &ln))
	body := make([]byte, ln)
	_, err := io.ReadFull(conn, body)
	require.NoError(t, err)
	reqID = int32(binary.LittleEndian.Uint32(body[0:4]))
	typ = int32(binary.LittleEndian.Uint32(body[4:8]))
	return reqID, typ, string(body[8 : len(body)-2])
}

func writeRCONFrame(conn net.Conn, reqID, typ int32, payload string) {
	b := make([]byte, 0, 14+len(payload))
	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(reqID))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(typ))
	b = append(b, hdr...)
	b = append(b, []byte(payload)...)
	b = append(b, 0, 0)
	out := make([]byte, 4+len(b))
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(b)))
	copy(out[4:], b)
	_, _ = conn.Write(out)
}

// fakeRCONServer 模拟 Minecraft RCON：要求首帧为 SERVERDATA_AUTH(类型 3) 才鉴权，
// 鉴权前的命令帧返回 reqID=-1。返回监听端口与「实际收到的鉴权帧类型」通道。
func fakeRCONServer(t *testing.T, authOK bool) (port int, gotAuthType chan int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gotAuthType = make(chan int32, 1)
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reqID, typ, _ := readRCONFrame(t, conn) // 第一帧：应为鉴权
		gotAuthType <- typ
		authenticated := typ == 3 && authOK
		if authenticated {
			writeRCONFrame(conn, reqID, 2, "") // 鉴权成功：回原 reqID
		} else {
			writeRCONFrame(conn, -1, 2, "") // 鉴权失败：reqID = -1
			return
		}
		cReqID, _, cmd := readRCONFrame(t, conn) // 第二帧：命令
		writeRCONFrame(conn, cReqID, 0, "执行:"+cmd)
	}()
	return ln.Addr().(*net.TCPAddr).Port, gotAuthType
}

// TestRCONClient_AuthUsesType3 回归测试：鉴权帧必须为 SERVERDATA_AUTH(类型 3)。
// 此前误用类型 2(EXECCOMMAND) 导致连接从未鉴权、命令被服务端忽略却上报成功
// （kick/ban/whitelist 形同空操作，真机复验 FR-054 时发现）。
func TestRCONClient_AuthUsesType3(t *testing.T) {
	port, gotAuthType := fakeRCONServer(t, true)
	c := NewRCONClient("127.0.0.1", port, "secret")
	require.NoError(t, c.Connect(), "鉴权应成功")
	defer c.Close()

	assert.Equal(t, int32(3), <-gotAuthType, "鉴权帧类型必须为 3(SERVERDATA_AUTH)")

	out, err := c.SendCommand("kick Steve")
	require.NoError(t, err)
	assert.Equal(t, "执行:kick Steve", out, "鉴权后命令应被服务端执行并回包")
}

// TestRCONClient_AuthFailureRejected 回归测试：服务端回 reqID=-1（密码错）时 Connect 必须报错，
// 不得静默当作连接成功（旧实现忽略 reqID 导致后续命令静默失败）。
func TestRCONClient_AuthFailureRejected(t *testing.T) {
	port, _ := fakeRCONServer(t, false)
	c := NewRCONClient("127.0.0.1", port, "wrong")
	defer c.Close()
	assert.Error(t, c.Connect(), "鉴权失败(reqID=-1)时 Connect 应返回错误")
}

// TestQueryInstanceMetrics_Unreachable 验证 RCON 不可达时优雅降级返回 -1 标记值。
func TestQueryInstanceMetrics_Unreachable(t *testing.T) {
	// 使用一个几乎不可能有服务监听的高位端口
	tps, players, err := QueryInstanceMetrics("127.0.0.1", 19999, "test")

	assert.NoError(t, err, "优雅降级不应返回 error")
	assert.Equal(t, float32(-1), tps, "RCON 不可达时 TPS 应为 -1")
	assert.Equal(t, int32(-1), players, "RCON 不可达时在线玩家应为 -1")
}

// TestQueryInstanceMetrics_InvalidHost 验证无效主机地址同样优雅降级。
func TestQueryInstanceMetrics_InvalidHost(t *testing.T) {
	tps, players, err := QueryInstanceMetrics("192.0.2.1", 25575, "test")

	assert.NoError(t, err, "优雅降级不应返回 error")
	assert.Equal(t, float32(-1), tps)
	assert.Equal(t, int32(-1), players)
}
