package top.wcpe.mc.jm.updater.core;

import java.awt.BorderLayout;
import java.awt.Dimension;
import java.awt.event.WindowAdapter;
import java.awt.event.WindowEvent;

import javax.swing.BorderFactory;
import javax.swing.BoxLayout;
import javax.swing.JFrame;
import javax.swing.JLabel;
import javax.swing.JPanel;
import javax.swing.JProgressBar;
import javax.swing.SwingUtilities;
import javax.swing.Timer;

/**
 * 独立 Swing 进度窗口（FR-099）：进度条 + 速度 + ETA + 当前文件名，更新期弹出、下载完关闭。
 *
 * <p>UI 构建/刷新全在 EDT；{@link Timer} 每 200ms 读 {@link ProgressModel} 刷新（即便下载停滞，
 * 速度/ETA 仍随时间回落）。玩家关窗 → {@link #isCancelled()} 置真（reconcile 据此停下载、fail-static）。
 * 全程 try/catch fail-open：任何 Swing 异常都不阻断更新/游戏。
 */
final class SwingProgressView implements ProgressView {

    private final ProgressModel model;
    private final CoreMessages msg;
    private final Logger log;

    private volatile boolean cancelled;
    private volatile boolean closed;

    // 仅 EDT 访问。
    private JFrame frame;
    private JProgressBar bar;
    private JLabel detail;
    private Timer timer;

    SwingProgressView(ProgressModel model, CoreMessages msg, Logger log) {
        this.model = model;
        this.msg = msg;
        this.log = log;
    }

    @Override
    public void show() {
        // 必须 invokeAndWait（同步）：楔子 CoreLoader 在 Core.run 返回后即关闭加载 core 的 URLClassLoader，
        // 若用 invokeLater（异步），build() 在 EDT 上懒加载匿名内部类（WindowAdapter 等）时 classloader 已关，
        // NoClassDefFoundError → 窗口建不出来。同步构建保证类加载在 run() 返回前、classloader 仍开时完成。
        runOnEdtAndWait(new Runnable() {
            @Override
            public void run() {
                if (closed || frame != null) {
                    return;
                }
                try {
                    build();
                } catch (Throwable t) {
                    log.warn("进度窗口显示失败（忽略，继续更新）: " + t);
                }
            }
        });
    }

    private void build() {
        JFrame f = new JFrame(msg.title());
        f.setDefaultCloseOperation(JFrame.DO_NOTHING_ON_CLOSE);
        f.addWindowListener(new WindowAdapter() {
            @Override
            public void windowClosing(WindowEvent e) {
                cancelled = true; // 玩家请求取消 → reconcile 停下载、fail-static 放行。
                disposeNow();
            }
        });

        JPanel panel = new JPanel();
        panel.setBorder(BorderFactory.createEmptyBorder(14, 16, 14, 16));
        panel.setLayout(new BoxLayout(panel, BoxLayout.Y_AXIS));

        JLabel status = new JLabel(msg.downloading());
        status.setAlignmentX(JPanel.LEFT_ALIGNMENT);

        bar = new JProgressBar(0, 100);
        bar.setStringPainted(true);
        bar.setAlignmentX(JPanel.LEFT_ALIGNMENT);
        bar.setMaximumSize(new Dimension(Integer.MAX_VALUE, 22));

        detail = new JLabel(" ");
        detail.setAlignmentX(JPanel.LEFT_ALIGNMENT);

        panel.add(status);
        panel.add(javax.swing.Box.createVerticalStrut(8));
        panel.add(bar);
        panel.add(javax.swing.Box.createVerticalStrut(8));
        panel.add(detail);

        f.getContentPane().add(panel, BorderLayout.CENTER);
        f.setSize(460, 160);
        f.setResizable(false);
        f.setLocationRelativeTo(null);
        f.setAlwaysOnTop(true);
        this.frame = f;
        f.setVisible(true);

        timer = new Timer(200, e -> render());
        timer.setRepeats(true);
        timer.start();
        render();
    }

    /** EDT：把 model 当前态画到组件上。 */
    private void render() {
        if (bar == null) {
            return;
        }
        try {
            long now = System.currentTimeMillis();
            int pct = (int) Math.round(model.fraction() * 100);
            bar.setValue(pct);
            long spd = model.speedBytesPerSec(now);
            long eta = model.etaSeconds(now);
            String file = shortName(model.currentFile());
            String line = file.isEmpty()
                    ? CoreMessages.humanSpeed(spd) + "  ·  " + msg.eta(eta)
                    : file + "   " + CoreMessages.humanSpeed(spd) + "  ·  " + msg.eta(eta);
            detail.setText(line);
        } catch (Throwable t) {
            // 刷新失败不致命：停表避免反复异常。
            if (timer != null) {
                timer.stop();
            }
        }
    }

    @Override
    public void onProgress() {
        // Swing 由定时器驱动刷新，此处无需动作。
    }

    @Override
    public boolean isCancelled() {
        return cancelled;
    }

    @Override
    public void close() {
        closed = true;
        // 同步关闭：确保停表 + 销毁窗在 Core.run 返回（classloader 关闭）前完成，EDT 不留后续任务。
        runOnEdtAndWait(new Runnable() {
            @Override
            public void run() {
                disposeNow();
            }
        });
    }

    /** 在 EDT 同步执行（已在 EDT 则直接跑）；调度异常 fail-open 吞掉。 */
    private static void runOnEdtAndWait(Runnable r) {
        try {
            if (SwingUtilities.isEventDispatchThread()) {
                r.run();
            } else {
                SwingUtilities.invokeAndWait(r);
            }
        } catch (Throwable t) {
            // EDT 调度失败不致命（fail-open）：宁可不显示进度，也不阻断更新/游戏。
        }
    }

    /** EDT：停表 + 销毁窗口（幂等）。 */
    private void disposeNow() {
        try {
            if (timer != null) {
                timer.stop();
                timer = null;
            }
            if (frame != null) {
                frame.dispose();
                frame = null;
            }
        } catch (Throwable ignore) {
            // 销毁失败无害。
        }
    }

    /** 仅取末段文件名。 */
    private static String shortName(String path) {
        if (path == null || path.isEmpty()) {
            return "";
        }
        int i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
        return i >= 0 && i < path.length() - 1 ? path.substring(i + 1) : path;
    }
}
