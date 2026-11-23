// 验收缺口补足的**最小游戏服夹具**（真实 java 进程，非 sleep 假进程）。
//
// 为什么必须有它：收养（adopt）链路的三个下游验收项——心跳报 RUNNING、健康巡检覆盖、
// Stop 可用——此前的端到端用例要么只断言内存表快照，要么拿 `sleep` 当替身。`sleep` 与真实
// MC 服务端的差别恰好落在被判定的那几处：它不写日志（采集侧观察不到）、不读 stdin（wrapper
// 的 `stop` 控制帧无人消费 → 只能等超时强杀）、不响应 SIGTERM（优雅退出链路完全没被走到）、
// 不监听端口（响应维度探针恒失败）。用它做替身等于把「链路是否真的通了」换成了「函数是否
// 被调到了」。
//
// 约束（刻意）：只用 JDK 自带 API、Java 8 兼容、不依赖任何 MC 服务端 jar。CI 上没有 java
// 时用例直接 t.Skip——夹具本身不做任何环境探测。
//
// 行为（与真实 MC 服务端的可观测特征一一对应）：
//   - 启动后周期性向 stdout 追加日志行（模拟服务端日志），供采集/Terminal 侧观察；
//   - 读 stdin 的 `stop` 后走优雅关服序列（打印 save-all / stopping）再退出——wrapper 的
//     `stop` 控制帧因此能被消费，停止不再是「等到超时被强杀」；
//   - 注册 SIGTERM shutdown hook，收到信号同样走优雅关服序列（direct/强杀路径验收用）；
//   - `--port N` 时在 127.0.0.1:N 上起最小 HTTP 服务，`/metrics` 返回 200——健康巡检的响应
//     维度（HTTPHealthProbe）因此有真实可达目标；不给 --port 则完全不起监听（不可达形态）。
//   - `--log <file>` 时把每一行同时追写进该文件（模拟真实服务端自己的 logs/latest.log）。
//     为什么需要：真实 MC 的日志采集读的是**实例目录下的日志文件**，而不是进程 stdout；
//     stdout 在 wrapper 链路里只经控制连接转发，而控制连接在 wrapper 收到 stop 后立即断开，
//     停止过程的最后几行往往来不及转发。沿用 stdout 做断言会把「采集还没看到」误判成
//     「停止没走控制帧」。直接落盘才是与服务端一致的观测面。
//
// 用法：java -cp <classes> MinecraftServerStub [--port <n>] [--name <name>]

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;

import java.io.BufferedReader;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.io.PrintWriter;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

public final class MinecraftServerStub {

    private static final AtomicBoolean stopping = new AtomicBoolean(false);
    private static volatile HttpServer httpServer;
    private static volatile String name = "stub";
    private static volatile PrintWriter logFile;

    public static void main(String[] args) throws Exception {
        int port = 0;
        String logPath = null;
        for (int i = 0; i < args.length; i++) {
            if ("--port".equals(args[i]) && i + 1 < args.length) {
                port = Integer.parseInt(args[++i]);
            } else if ("--name".equals(args[i]) && i + 1 < args.length) {
                name = args[++i];
            } else if ("--log".equals(args[i]) && i + 1 < args.length) {
                logPath = args[++i];
            }
        }
        if (logPath != null) {
            // 追加写：模拟服务端自己的日志文件（重启时不清空，与常见 MC 日志行为一致）。
            logFile = new PrintWriter(new FileOutputStream(logPath, true), true);
        }

        // shutdown hook：SIGTERM / SIGINT（wrapper 的兜底信号路径）走同一条优雅关服序列。
        Runtime.getRuntime().addShutdownHook(new Thread(new Runnable() {
            @Override
            public void run() {
                shutdown("SIGTERM-hook");
            }
        }));

        log("Starting minecraft server stub " + name);
        log("Done (0.421s)! For help, type \"help\"");

        if (port > 0) {
            startHttp(port);
            log("Metrics endpoint listening on 127.0.0.1:" + port);
        }

        // 周期性日志：模拟真实服务端的持续输出，供采集侧与终端观察（也证明进程真的在跑）。
        final ScheduledExecutorService ticker = Executors.newSingleThreadScheduledExecutor();
        ticker.scheduleAtFixedRate(new Runnable() {
            private int n = 0;

            @Override
            public void run() {
                if (stopping.get()) {
                    return;
                }
                n++;
                log("[Server thread/INFO] tick " + n + " players=0 tps=20.0");
            }
        }, 1, 1, TimeUnit.SECONDS);

        // stdin 控制台：真实 MC 服务端常驻读 stdin，wrapper 的 stop 帧即写在这里。
        // 读到 EOF 也退出（wrapper 关闭 stdin 管道是它的停止意图）。
        Thread console = new Thread(new Runnable() {
            @Override
            public void run() {
                try {
                    InputStream in = System.in;
                    BufferedReader reader = new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8));
                    String line;
                    while ((line = reader.readLine()) != null) {
                        String cmd = line.trim();
                        if (cmd.isEmpty()) {
                            continue;
                        }
                        log("[Server thread/INFO] console command: " + cmd);
                        if ("stop".equalsIgnoreCase(cmd) || "end".equalsIgnoreCase(cmd)) {
                            shutdown("console-" + cmd);
                            break;
                        }
                    }
                    // EOF：wrapper 关掉了 stdin 管道（停止意图），按优雅关服处理。
                    shutdown("stdin-eof");
                } catch (IOException e) {
                    shutdown("stdin-error");
                }
            }
        });
        console.setDaemon(true);
        console.setName("console");
        console.start();

        // 主线程等待关服完成。
        while (!stopping.get()) {
            Thread.sleep(100);
        }
        ticker.shutdownNow();
        log("Server stub stopped");
        // 刻意用显式退出：shutdown hook 已经跑过，不再依赖 JVM 收尾判定。
        Runtime.getRuntime().halt(0);
    }

    private static void shutdown(String reason) {
        if (!stopping.compareAndSet(false, true)) {
            return;
        }
        log("[Server thread/INFO] Stopping the server (" + reason + ")");
        log("[Server thread/INFO] Saving worlds");
        log("[Server thread/INFO] Saving chunks for level 'world'/overworld");
        HttpServer srv = httpServer;
        if (srv != null) {
            srv.stop(0);
        }
        log("[Server thread/INFO] Saving worlds complete");
    }

    private static void startHttp(int port) throws IOException {
        HttpServer srv = HttpServer.create(new InetSocketAddress("127.0.0.1", port), 0);
        srv.createContext("/metrics", new HttpHandler() {
            @Override
            public void handle(HttpExchange ex) throws IOException {
                byte[] body = ("# stub metrics\nstub_up 1\nstub_name{" + name + "} 1\n")
                        .getBytes(StandardCharsets.UTF_8);
                ex.getResponseHeaders().set("Content-Type", "text/plain; charset=utf-8");
                ex.sendResponseHeaders(200, body.length);
                OutputStream os = ex.getResponseBody();
                os.write(body);
                os.close();
            }
        });
        srv.setExecutor(Executors.newSingleThreadExecutor());
        srv.start();
        httpServer = srv;
    }

    private static void log(String line) {
        System.out.println(line);
        System.out.flush();
        PrintWriter f = logFile;
        if (f != null) {
            f.println(line);
            f.flush();
        }
    }
}
