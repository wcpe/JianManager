// Package setup 实现 Worker「免配置自启 setup」（FR-222，见 ADR-051）。
//
// Worker 入口在加载配置前自检「是否已配置」（无 worker.yml/.yaml 且无 etc/node-identity.json）；
// 未配置即调用本包：采集上线入参（有 TTY 交互逐项问、无 TTY 从命令行参数 + 环境变量读）→
// 原子写 worker.yml（enrollment token 绝不落盘）→ 携 token 经 gRPC 首注册换身份 →
// 持久化 node_uuid/node_secret 到 etc/node-identity.json（0600）→ 把内存配置 + 首注册身份
// 交回入口转入正常 run（不重启进程、不重复注册）。
//
// 「下载」（取二进制）与「上线」（setup + 注册 + run）解耦：Worker 自己负责上线，下载归外部步骤。
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/register"
)

// 默认值（与 config 包默认、install-worker.sh 写出的字段一致）。
const (
	defaultControlPlane = "localhost:9100"
	defaultGRPCPort     = 9101
	defaultWSPort       = 9102
)

// Inputs 是 setup 采集到的上线入参。
type Inputs struct {
	// ControlPlane CP gRPC 地址 host:port（必填）。
	ControlPlane string
	// EnrollToken enrollment token 明文（必填，仅本次注册用，绝不写入 worker.yml）。
	EnrollToken string
	// NodeName 节点名（可空，留空由 CP/token 预设名生效）。
	NodeName string
	// GRPCPort / WSPort Worker gRPC / WS 端口。
	GRPCPort int
	WSPort   int
	// DataDir 数据根（空 = 默认 ./data，缺省不写入 worker.yml）。
	DataDir string
}

// Result 是 setup 的产物：转入正常 run 所需的内存配置与首注册身份。
type Result struct {
	// Config setup 写出并在内存构造的 Worker 配置（入口据此转 run，无需重读文件）。
	Config *workercfg.Config
	// Identity 首注册换得的节点身份（已持久化到 etc/node-identity.json）。
	// 入口据此跳过重复注册，直接以该身份起服务与心跳。
	Identity *register.Identity
}

// Options 控制 setup 行为（注入便于测试）。
type Options struct {
	// Args 命令行参数（不含程序名与配置文件位置参数），用于无 TTY 形态解析 --xxx flag。
	Args []string
	// In 交互输入源（默认 os.Stdin）。
	In io.Reader
	// Out 提示输出（默认 os.Stderr，避免污染 stdout）。
	Out io.Writer
	// IsTTY 是否交互式终端（默认探测 os.Stdin）。
	IsTTY bool
	// Registrar 执行注册（默认走 register.RegisterWithRetry）。注入便于测试。
	Registrar func(ctx context.Context, cfg register.Config) (*register.Result, error)
}

// Run 执行完整 setup 流程并返回转 run 所需的配置与身份（FR-222，见 ADR-051）。
//
// configWorkDir 是写 worker.yml 的目标目录（通常为进程工作目录 "."）。
func Run(ctx context.Context, configWorkDir string, opts Options) (*Result, error) {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	if opts.Registrar == nil {
		opts.Registrar = func(ctx context.Context, cfg register.Config) (*register.Result, error) {
			return register.RegisterWithRetry(ctx, cfg, 2*time.Second, 60*time.Second)
		}
	}

	in, err := CollectInputs(opts)
	if err != nil {
		return nil, err
	}

	// 1. 写 worker.yml（原子，enrollment token 绝不写入）。
	ymlPath := filepath.Join(configWorkDir, "worker.yml")
	if _, statErr := os.Stat(ymlPath); statErr == nil {
		// 自检理论上已排除（有 yml 即已配置）；并发/竞态防御：不覆盖既有配置。
		return nil, fmt.Errorf("worker.yml 已存在，拒绝覆盖: %s", ymlPath)
	}
	if err := WriteWorkerYML(ymlPath, in); err != nil {
		return nil, fmt.Errorf("写 worker.yml 失败: %w", err)
	}
	fmt.Fprintf(opts.Out, "已写配置: %s（enrollment token 未写入）\n", ymlPath)

	// 2. 携 enrollment token 首注册（走 gRPC，token 经 metadata，不改 proto）。
	regCfg := register.Config{
		ControlPlaneAddr: in.ControlPlane,
		NodeName:         in.NodeName,
		WsPort:           in.WSPort,
		GrpcPort:         in.GRPCPort,
		EnrollToken:      in.EnrollToken,
	}
	fmt.Fprintf(opts.Out, "正在向 Control Plane 注册（%s）...\n", in.ControlPlane)
	regResult, err := opts.Registrar(ctx, regCfg)
	if err != nil {
		return nil, fmt.Errorf("首次注册失败: %w", err)
	}

	// 3. 持久化身份到 <data-dir>/etc/node-identity.json（0600 原子写）。
	root, err := dataroot.Resolve(in.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析数据根失败: %w", err)
	}
	identity := &register.Identity{
		NodeUUID:   regResult.NodeUUID,
		NodeSecret: regResult.NodeSecret,
		NodeName:   in.NodeName,
	}
	if err := register.SaveIdentity(root.EtcDir(), identity); err != nil {
		return nil, fmt.Errorf("持久化节点身份失败: %w", err)
	}
	fmt.Fprintf(opts.Out, "注册成功，节点身份已持久化: %s\n", register.IdentityPath(root.EtcDir()))

	// 4. 内存构造配置交回入口转 run（不重读文件、不重启进程）。
	cfg, err := workercfg.Load(ymlPath)
	if err != nil {
		return nil, fmt.Errorf("加载新写出的 worker.yml 失败: %w", err)
	}
	return &Result{Config: cfg, Identity: identity}, nil
}

// CollectInputs 按形态采集上线入参：有 TTY 交互逐项问、无 TTY 从命令行参数 + 环境变量读。
func CollectInputs(opts Options) (*Inputs, error) {
	if opts.IsTTY {
		return collectInteractive(opts)
	}
	return collectNonInteractive(opts)
}

// IsStdinTTY 探测标准输入是否为交互式终端。
func IsStdinTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// collectNonInteractive 从命令行参数（优先）+ 环境变量读入参，缺必填项即报错（不阻塞等输入）。
// 无 TTY（CI / 管道 / systemd / Windows 服务）路径：fail-fast 而非死锁。
func collectNonInteractive(opts Options) (*Inputs, error) {
	flags := parseFlags(opts.Args)

	pick := func(flagVal string, envKeys ...string) string {
		if flagVal != "" {
			return flagVal
		}
		for _, k := range envKeys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}

	in := &Inputs{
		ControlPlane: pick(flags["control-plane"], "JIANMANAGER_CONTROL_PLANE", "JIANMANAGER_CONTROL_PLANE_GRPC"),
		EnrollToken:  pick(flags["token"], "JIANMANAGER_ENROLL_TOKEN"),
		NodeName:     pick(flags["name"], "JIANMANAGER_NODE_NAME"),
		DataDir:      pick(flags["data-dir"], "JIANMANAGER_DATA_DIR"),
	}

	grpcPort, err := pickPort(flags["grpc-port"], "JIANMANAGER_GRPC_PORT", defaultGRPCPort)
	if err != nil {
		return nil, fmt.Errorf("--grpc-port 非法: %w", err)
	}
	wsPort, err := pickPort(flags["ws-port"], "JIANMANAGER_WS_PORT", defaultWSPort)
	if err != nil {
		return nil, fmt.Errorf("--ws-port 非法: %w", err)
	}
	in.GRPCPort = grpcPort
	in.WSPort = wsPort

	if in.ControlPlane == "" {
		in.ControlPlane = defaultControlPlane
	}

	// 无 TTY 下必填项缺失 → 明确报错退出（指明怎么补），不卡住。
	var missing []string
	if in.EnrollToken == "" {
		missing = append(missing, "enrollment token（--token 或环境变量 JIANMANAGER_ENROLL_TOKEN）")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Worker 未配置且无可用入参（无交互式终端）：缺少 %s。"+
			"请在面板「添加节点」生成 token 后，以 --token / 环境变量提供，或在交互式终端运行以逐项填写",
			strings.Join(missing, "、"))
	}

	return in, nil
}

// collectInteractive 在 TTY 下逐项提示采集入参（给默认值、回车接受默认、token 必填）。
func collectInteractive(opts Options) (*Inputs, error) {
	r := bufio.NewReader(opts.In)
	w := opts.Out

	fmt.Fprintln(w, "未检测到配置，进入节点上线向导（FR-222）。逐项填写后将写 worker.yml、注册并上线。")

	flags := parseFlags(opts.Args) // 命令行已给的项作为交互默认值

	cp, err := promptLine(r, w, "Control Plane gRPC 地址", firstNonEmpty(flags["control-plane"], os.Getenv("JIANMANAGER_CONTROL_PLANE"), defaultControlPlane), false)
	if err != nil {
		return nil, err
	}
	token, err := promptToken(r, w, firstNonEmpty(flags["token"], os.Getenv("JIANMANAGER_ENROLL_TOKEN")))
	if err != nil {
		return nil, err
	}
	name, err := promptLine(r, w, "节点名（可空，留空由平台分配）", firstNonEmpty(flags["name"], os.Getenv("JIANMANAGER_NODE_NAME")), true)
	if err != nil {
		return nil, err
	}
	grpcPort, err := promptPort(r, w, "gRPC 端口", firstNonEmpty(flags["grpc-port"], os.Getenv("JIANMANAGER_GRPC_PORT")), defaultGRPCPort)
	if err != nil {
		return nil, err
	}
	wsPort, err := promptPort(r, w, "WS 终端端口", firstNonEmpty(flags["ws-port"], os.Getenv("JIANMANAGER_WS_PORT")), defaultWSPort)
	if err != nil {
		return nil, err
	}
	dataDir, err := promptLine(r, w, "数据根目录（可空=./data）", firstNonEmpty(flags["data-dir"], os.Getenv("JIANMANAGER_DATA_DIR")), true)
	if err != nil {
		return nil, err
	}

	return &Inputs{
		ControlPlane: cp,
		EnrollToken:  token,
		NodeName:     name,
		GRPCPort:     grpcPort,
		WSPort:       wsPort,
		DataDir:      dataDir,
	}, nil
}

// parseFlags 解析 setup 的 --key value / --key=value 具名参数（忽略未知项与位置参数）。
func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	known := map[string]bool{
		"control-plane": true, "token": true, "name": true,
		"grpc-port": true, "ws-port": true, "data-dir": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			val := key[eq+1:]
			key = key[:eq]
			if known[key] {
				out[key] = val
			}
			continue
		}
		if !known[key] {
			continue
		}
		// --key value 形态：取下一个非 flag 实参作值。
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			out[key] = args[i+1]
			i++
		} else {
			out[key] = ""
		}
	}
	return out
}

func pickPort(flagVal, envKey string, def int) (int, error) {
	raw := flagVal
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envKey))
	}
	if raw == "" {
		return def, nil
	}
	return parsePort(raw)
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("端口需为数字: %q", s)
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("端口越界（1~65535）: %d", p)
	}
	return p, nil
}

// promptLine 提示一行输入；required=false 时空输入回退默认值。EOF 视为无法继续。
func promptLine(r *bufio.Reader, w io.Writer, label, def string, allowEmpty bool) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(w, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(w, "%s: ", label)
		}
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("读取输入失败: %w", err)
		}
		val := strings.TrimSpace(line)
		if val == "" {
			if def != "" {
				return def, nil
			}
			if allowEmpty {
				return "", nil
			}
		}
		if val != "" {
			return val, nil
		}
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("输入流结束（EOF），无法完成 %s 采集", label)
		}
		fmt.Fprintln(w, "  必填，请输入")
	}
}

// promptToken 提示 enrollment token（必填，空则重问）。EOF 报错退出。
func promptToken(r *bufio.Reader, w io.Writer, def string) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(w, "Enrollment token（面板「添加节点」生成）[已由参数提供，回车沿用]: ")
		} else {
			fmt.Fprintf(w, "Enrollment token（面板「添加节点」生成）: ")
		}
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("读取 token 失败: %w", err)
		}
		val := strings.TrimSpace(line)
		if val == "" && def != "" {
			return def, nil
		}
		if val != "" {
			return val, nil
		}
		if errors.Is(err, io.EOF) {
			return "", errors.New("输入流结束（EOF），enrollment token 未提供")
		}
		fmt.Fprintln(w, "  enrollment token 必填，请粘贴面板生成的 jmet_ 开头 token")
	}
}

// promptPort 提示端口输入，非法重问，空回退默认。
func promptPort(r *bufio.Reader, w io.Writer, label, def string, fallback int) (int, error) {
	defStr := def
	if defStr == "" {
		defStr = strconv.Itoa(fallback)
	}
	for {
		fmt.Fprintf(w, "%s [%s]: ", label, defStr)
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("读取端口失败: %w", err)
		}
		val := strings.TrimSpace(line)
		if val == "" {
			p, perr := parsePort(defStr)
			if perr != nil {
				return fallback, nil
			}
			return p, nil
		}
		p, perr := parsePort(val)
		if perr == nil {
			return p, nil
		}
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("输入流结束（EOF），端口未采集: %w", perr)
		}
		fmt.Fprintf(w, "  %v，请重新输入\n", perr)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
