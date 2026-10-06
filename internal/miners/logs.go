package miners

import (
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/yorgof/miner-fleet/internal/store"
	"net"
	"net/http"
	"strings"
	"time"
)

// nerdLogs captures five seconds of the installed firmware's live log stream.
func (m *Manager) nerdLogs(ctx context.Context, miner store.Miner, cred store.Credentials) ([]byte, error) {
	headers := http.Header{}
	if cred.SessionToken != "" {
		headers.Set("X-OTP-Session", cred.SessionToken)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.DialContext(ctx, strings.Replace(DeviceURL(miner, cred), "http://", "ws://", 1)+"/api/ws", headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("miner log stream unavailable")
	}
	defer conn.Close()
	defer conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(6*time.Second))
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetReadDeadline(deadline)
	conn.SetReadLimit(64 << 10)
	var out []byte
	for len(out) < 1<<20 {
		_, b, err := conn.ReadMessage()
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				break
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			return nil, fmt.Errorf("miner log stream interrupted")
		}
		out = append(out, b...)
	}
	if len(out) == 0 {
		out = []byte("No log messages during the five-second capture.\n")
	}
	return out, nil
}
