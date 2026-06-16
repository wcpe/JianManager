package metrics

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// RCONClient RCON 客户端。
type RCONClient struct {
	host     string
	port     int
	password string
	conn     net.Conn
}

// NewRCONClient 创建 RCON 客户端。
func NewRCONClient(host string, port int, password string) *RCONClient {
	return &RCONClient{
		host:     host,
		port:     port,
		password: password,
	}
}

// Connect 连接到 RCON 服务器。
func (c *RCONClient) Connect() error {
	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("RCON 连接失败: %w", err)
	}
	c.conn = conn

	// 发送认证请求
	if err := c.sendPacket(3, 2, c.password); err != nil {
		return err
	}

	// 读取认证响应
	_, _, _, err = c.readPacket()
	if err != nil {
		return fmt.Errorf("RCON 认证失败: %w", err)
	}

	return nil
}

// Close 关闭连接。
func (c *RCONClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// SendCommand 发送 RCON 命令。
func (c *RCONClient) SendCommand(command string) (string, error) {
	if c.conn == nil {
		return "", fmt.Errorf("RCON 未连接")
	}

	if err := c.sendPacket(0, 2, command); err != nil {
		return "", err
	}

	_, _, body, err := c.readPacket()
	if err != nil {
		return "", err
	}

	return body, nil
}

// QueryTPS 查询服务器 TPS。
func (c *RCONClient) QueryTPS() (float64, error) {
	resp, err := c.SendCommand("tps")
	if err != nil {
		return 0, err
	}

	// 解析 TPS 响应（格式通常为 "TPS: 20.0" 或 "§a20.0§f, §a20.0§f, §a20.0§f"）
	// 清理颜色代码
	cleaned := cleanMinecraftColors(resp)

	// 尝试提取数字
	parts := strings.Split(cleaned, ",")
	if len(parts) > 0 {
		last := strings.TrimSpace(parts[len(parts)-1])
		last = strings.TrimPrefix(last, "TPS:")
		last = strings.TrimSpace(last)

		tps, err := strconv.ParseFloat(last, 64)
		if err == nil {
			return tps, nil
		}
	}

	// 如果无法解析，返回默认值
	return 20.0, nil
}

// QueryOnlinePlayers 查询在线玩家。
func (c *RCONClient) QueryOnlinePlayers() (int, []string, error) {
	resp, err := c.SendCommand("list")
	if err != nil {
		return 0, nil, err
	}

	// 解析玩家列表响应（格式通常为 "There are X of a max of Y players online: player1, player2"）
	cleaned := cleanMinecraftColors(resp)

	// 提取玩家数
	if idx := strings.Index(cleaned, "There are "); idx >= 0 {
		rest := cleaned[idx+10:]
		if spaceIdx := strings.Index(rest, " "); spaceIdx >= 0 {
			count, err := strconv.Atoi(rest[:spaceIdx])
			if err == nil {
				// 提取玩家名
				var players []string
				if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
					playerList := strings.TrimSpace(rest[colonIdx+1:])
					if playerList != "" {
						players = strings.Split(playerList, ", ")
					}
				}
				return count, players, nil
			}
		}
	}

	return 0, nil, nil
}

func (c *RCONClient) sendPacket(requestID int32, packetType int32, payload string) error {
	// RCON 包格式: [长度(4)] [请求ID(4)] [类型(4)] [载荷] [空字节(2)]
	payloadBytes := []byte(payload)
	length := int32(4 + 4 + len(payloadBytes) + 2)

	buf := make([]byte, 4+length)
	// 长度
	buf[0] = byte(length)
	buf[1] = byte(length >> 8)
	buf[2] = byte(length >> 16)
	buf[3] = byte(length >> 24)
	// 请求 ID
	buf[4] = byte(requestID)
	buf[5] = byte(requestID >> 8)
	buf[6] = byte(requestID >> 16)
	buf[7] = byte(requestID >> 24)
	// 类型
	buf[8] = byte(packetType)
	buf[9] = byte(packetType >> 8)
	buf[10] = byte(packetType >> 16)
	buf[11] = byte(packetType >> 24)
	// 载荷
	copy(buf[12:], payloadBytes)
	// 空字节
	buf[len(buf)-2] = 0
	buf[len(buf)-1] = 0

	_, err := c.conn.Write(buf)
	return err
}

func (c *RCONClient) readPacket() (int32, int32, string, error) {
	// 读取长度
	lenBuf := make([]byte, 4)
	if _, err := readFull(c.conn, lenBuf); err != nil {
		return 0, 0, "", err
	}
	length := int32(lenBuf[0]) | int32(lenBuf[1])<<8 | int32(lenBuf[2])<<16 | int32(lenBuf[3])<<24

	if length < 10 || length > 4096 {
		return 0, 0, "", fmt.Errorf("RCON 包长度异常: %d", length)
	}

	// 读取剩余数据
	body := make([]byte, length)
	if _, err := readFull(c.conn, body); err != nil {
		return 0, 0, "", err
	}

	requestID := int32(body[0]) | int32(body[1])<<8 | int32(body[2])<<16 | int32(body[3])<<24
	packetType := int32(body[4]) | int32(body[5])<<8 | int32(body[6])<<16 | int32(body[7])<<24

	// 载荷（去掉尾部两个空字节）
	payload := string(body[8 : len(body)-2])

	return requestID, packetType, payload, nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// cleanMinecraftColors 清理 Minecraft 颜色代码（§x）。
func cleanMinecraftColors(s string) string {
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '§' && i+1 < len(s) {
			i++ // 跳过颜色代码
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

// QueryInstanceMetrics 通过 RCON 查询实例指标。
// RCON 连接失败时返回 N/A 标记值（TPS=-1, OnlinePlayers=-1），调用方应据此显示 "N/A"。
func QueryInstanceMetrics(host string, rconPort int, rconPassword string) (tps float32, onlinePlayers int32, err error) {
	client := NewRCONClient(host, rconPort, rconPassword)
	defer client.Close()

	if err := client.Connect(); err != nil {
		slog.Debug("RCON 连接失败，返回 N/A", "host", host, "port", rconPort, "error", err)
		return -1, -1, nil // 优雅降级，返回 N/A 标记值
	}

	tpsVal, err := client.QueryTPS()
	if err != nil {
		slog.Warn("RCON 查询 TPS 失败", "error", err)
		tpsVal = -1
	}

	playerCount, _, err := client.QueryOnlinePlayers()
	if err != nil {
		slog.Warn("RCON 查询玩家列表失败", "error", err)
		playerCount = -1
	}

	return float32(tpsVal), int32(playerCount), nil
}
