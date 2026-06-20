package metrics

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProbeSnapshot 是从 ServerProbe `/metrics` 解析出的服务器运维指标子集。
//
// ServerProbe（第三方监控探针，作 git 子模块引入）以 Prometheus exposition 文本在
// `/metrics` 暴露 TPS/MSPT/JVM/线程/世界等指标；Worker 周期抓取本机端点并解析为本快照，
// 取代此前基于 RCON `list`/`tps` 文本解析的粗指标（FR-022）。
type ProbeSnapshot struct {
	TPS           float64              // serverprobe_tps{window="1m"}
	MSPTAvgMillis float64              // serverprobe_mspt_seconds{quantile="avg"} 转毫秒
	PlayersOnline int32                // serverprobe_players_online（代理端回退 proxy_players_online）
	HeapUsedBytes int64                // serverprobe_heap_used_bytes
	HeapMaxBytes  int64                // serverprobe_heap_max_bytes
	Threads       int32                // serverprobe_threads
	SystemCPULoad float64              // serverprobe_system_cpu_load（0~1，<0 表示不可用）
	UptimeSeconds float64              // serverprobe_uptime_seconds
	Worlds        map[string]WorldStat // 按世界名聚合的世界负载
}

// WorldStat 单个世界的负载（serverprobe_world_*{world="..."}）。
type WorldStat struct {
	LoadedChunks int64
	Entities     int64
	TileEntities int64
}

// promSample 一条 Prometheus 样本：指标名 + label 映射 + 数值。
type promSample struct {
	name   string
	labels map[string]string
	value  float64
}

// ScrapeServerProbe 抓取实例本机的 ServerProbe `/metrics` 并解析为指标快照。
// host 固定 localhost（探针与实例同机，与 RCON 一致）；token 非空时带 `Authorization: Bearer`。
// 抓取失败（端点未开/未装探针/鉴权失败）返回 error，调用方据此优雅降级。
func ScrapeServerProbe(host string, port int, token string) (*ProbeSnapshot, error) {
	url := fmt.Sprintf("http://%s:%d/metrics", host, port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ServerProbe /metrics 抓取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ServerProbe /metrics 返回状态 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseServerProbeMetrics(string(body)), nil
}

// parseServerProbeMetrics 解析 Prometheus 文本，提取关心的 serverprobe_* 指标。
// 纯函数、无 IO，便于对真实 /metrics 样本穷举测试。
func parseServerProbeMetrics(text string) *ProbeSnapshot {
	snap := &ProbeSnapshot{Worlds: map[string]WorldStat{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // 跳过空行与 # HELP/# TYPE 注释
		}
		s, ok := parsePromLine(line)
		if !ok || !strings.HasPrefix(s.name, "serverprobe_") {
			continue
		}
		switch s.name {
		case "serverprobe_tps":
			// 取 1 分钟窗口为代表 TPS；无 window 标签时也接受。
			if w := s.labels["window"]; w == "1m" || w == "" {
				snap.TPS = s.value
			}
		case "serverprobe_mspt_seconds":
			if s.labels["quantile"] == "avg" {
				snap.MSPTAvgMillis = s.value * 1000
			}
		case "serverprobe_players_online":
			snap.PlayersOnline = int32(s.value)
		case "serverprobe_proxy_players_online":
			// 代理端无 players_online，用代理总在线回退。
			if snap.PlayersOnline == 0 {
				snap.PlayersOnline = int32(s.value)
			}
		case "serverprobe_heap_used_bytes":
			snap.HeapUsedBytes = int64(s.value)
		case "serverprobe_heap_max_bytes":
			snap.HeapMaxBytes = int64(s.value)
		case "serverprobe_threads":
			snap.Threads = int32(s.value)
		case "serverprobe_system_cpu_load":
			snap.SystemCPULoad = s.value
		case "serverprobe_uptime_seconds":
			snap.UptimeSeconds = s.value
		case "serverprobe_world_loaded_chunks":
			w := snap.Worlds[s.labels["world"]]
			w.LoadedChunks = int64(s.value)
			snap.Worlds[s.labels["world"]] = w
		case "serverprobe_world_entities":
			w := snap.Worlds[s.labels["world"]]
			w.Entities = int64(s.value)
			snap.Worlds[s.labels["world"]] = w
		case "serverprobe_world_tile_entities":
			w := snap.Worlds[s.labels["world"]]
			w.TileEntities = int64(s.value)
			snap.Worlds[s.labels["world"]] = w
		}
	}
	return snap
}

// parsePromLine 解析一行 Prometheus exposition：`name{k="v",...} value` 或 `name value`。
func parsePromLine(line string) (promSample, bool) {
	s := promSample{labels: map[string]string{}}
	if brace := strings.IndexByte(line, '{'); brace >= 0 {
		end := strings.LastIndexByte(line, '}')
		if end <= brace {
			return s, false
		}
		s.name = line[:brace]
		parseLabels(line[brace+1:end], s.labels)
		v, err := strconv.ParseFloat(firstField(strings.TrimSpace(line[end+1:])), 64)
		if err != nil {
			return s, false
		}
		s.value = v
		return s, true
	}
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return s, false
	}
	s.name = line[:i]
	v, err := strconv.ParseFloat(firstField(strings.TrimSpace(line[i+1:])), 64)
	if err != nil {
		return s, false
	}
	s.value = v
	return s, true
}

// firstField 返回首个以空白分隔的字段（忽略可选的时间戳列）。
func firstField(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// parseLabels 解析 `k1="v1",k2="v2"`，按引号外的逗号切分，去掉值两端引号。
func parseLabels(s string, out map[string]string) {
	var b strings.Builder
	inQuote := false
	flush := func(part string) {
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			k := strings.TrimSpace(part[:eq])
			v := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
			out[k] = v
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
		}
		if c == ',' && !inQuote {
			flush(b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	if b.Len() > 0 {
		flush(b.String())
	}
}
