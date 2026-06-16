package service

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// TerminalProxy WebSocket 终端代理。
// 浏览器 → CP WebSocket → Worker WebSocket，双向桥接。
type TerminalProxy struct {
	jwtSecret string
	terminal  *TerminalService
	upgrader  websocket.Upgrader
}

// NewTerminalProxy 创建终端代理。
func NewTerminalProxy(jwtSecret string, terminal *TerminalService) *TerminalProxy {
	return &TerminalProxy{
		jwtSecret: jwtSecret,
		terminal:  terminal,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Handler 返回 HTTP handler，挂载到 /ws/terminal。
func (p *TerminalProxy) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 验证 JWT token
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			return []byte(p.jwtSecret), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "invalid claims", http.StatusUnauthorized)
			return
		}

		instanceUUID, _ := claims["instanceId"].(string)
		permission, _ := claims["permission"].(string)
		if instanceUUID == "" {
			http.Error(w, "missing instanceId", http.StatusBadRequest)
			return
		}

		// 2. 查找 Worker WS 地址
		workerWSURL, err := p.terminal.GetWorkerAddr(instanceUUID)
		if err != nil {
			slog.Error("查找 Worker 地址失败", "instanceUUID", instanceUUID, "error", err)
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}

		// 3. 升级浏览器连接
		browserConn, err := p.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer browserConn.Close()

		// 4. 连接 Worker WS（携带同样的 token）
		workerURL := fmt.Sprintf("%s?token=%s", workerWSURL, tokenStr)
		workerConn, _, err := websocket.DefaultDialer.Dial(workerURL, nil)
		if err != nil {
			slog.Error("连接 Worker WS 失败", "url", workerURL, "error", err)
			browserConn.WriteMessage(websocket.TextMessage,
				[]byte(fmt.Sprintf(`{"type":"state","state":"error","data":"连接 Worker 失败: %s"}`, err.Error())))
			return
		}
		defer workerConn.Close()

		slog.Info("终端代理已建立", "instanceUUID", instanceUUID, "permission", permission)

		// 5. 双向桥接
		var wg sync.WaitGroup
		wg.Add(2)

		// browser → worker（stdin、resize）
		go func() {
			defer wg.Done()
			defer workerConn.Close()
			for {
				msgType, msg, err := browserConn.ReadMessage()
				if err != nil {
					return
				}
				if err := workerConn.WriteMessage(msgType, msg); err != nil {
					return
				}
			}
		}()

		// worker → browser（stdout、stderr、state）
		go func() {
			defer wg.Done()
			defer browserConn.Close()
			for {
				msgType, msg, err := workerConn.ReadMessage()
				if err != nil {
					if err != io.EOF {
						slog.Debug("Worker WS 断开", "error", err)
					}
					return
				}
				if err := browserConn.WriteMessage(msgType, msg); err != nil {
					return
				}
			}
		}()

		wg.Wait()
		slog.Info("终端代理已关闭", "instanceUUID", instanceUUID)
	}
}
