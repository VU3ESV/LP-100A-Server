package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"lp100a-server/internal/meter"
)

const sendBuffer = 32

type Hub struct {
	upgrader     websocket.Upgrader
	serial       *meter.Owner
	snapIn       <-chan meter.Snapshot
	register     chan *client
	unregister   chan *client
	resync       chan *client
	heartbeat    time.Duration
	maxClients   int
	allowControl bool
	logger       *slog.Logger
	seq          atomic.Uint64
}

type client struct {
	conn *websocket.Conn
	send chan []byte
	addr string
}

type Options struct {
	Heartbeat    time.Duration
	MaxClients   int
	AllowControl bool
}

func NewHub(snapIn <-chan meter.Snapshot, serial *meter.Owner, opts Options, logger *slog.Logger) *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			// LAN-only deployment per PROPOSAL.md §3; any origin is accepted.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		serial:       serial,
		snapIn:       snapIn,
		register:     make(chan *client, 16),
		unregister:   make(chan *client, 16),
		resync:       make(chan *client, 16),
		heartbeat:    opts.Heartbeat,
		maxClients:   opts.MaxClients,
		allowControl: opts.AllowControl,
		logger:       logger,
	}
}

// Run owns the clients map and the last-known snapshot. All mutation happens
// on this single goroutine, so neither needs a lock.
func (h *Hub) Run(ctx context.Context) {
	clients := make(map[*client]struct{})
	var lastJSON []byte
	var lastSnap *meter.Snapshot
	lastSent := time.Now()

	hb := time.NewTicker(h.heartbeat)
	defer hb.Stop()

	closeAll := func() {
		for c := range clients {
			close(c.send)
		}
	}

	for {
		select {
		case <-ctx.Done():
			closeAll()
			return

		case c := <-h.register:
			if len(clients) >= h.maxClients {
				h.logger.Warn("max clients reached, rejecting", "addr", c.addr)
				close(c.send)
				continue
			}
			clients[c] = struct{}{}
			h.logger.Info("client connected", "addr", c.addr, "count", len(clients))
			if lastJSON != nil {
				select {
				case c.send <- lastJSON:
				default:
				}
			}

		case c := <-h.unregister:
			if _, ok := clients[c]; ok {
				delete(clients, c)
				close(c.send)
				h.logger.Info("client disconnected", "addr", c.addr, "count", len(clients))
			}

		case c := <-h.resync:
			if lastJSON != nil {
				select {
				case c.send <- lastJSON:
				default:
				}
			}

		case snap := <-h.snapIn:
			if lastSnap != nil && lastSnap.CloseEnough(snap) {
				continue
			}
			data, err := encodeTelemetry(snap, h.seq.Add(1))
			if err != nil {
				h.logger.Error("encode telemetry", "err", err)
				continue
			}
			lastSnap = &snap
			lastJSON = data
			lastSent = time.Now()
			h.broadcast(clients, data)

		case <-hb.C:
			if time.Since(lastSent) < h.heartbeat {
				continue
			}
			data, err := encodeHeartbeat(h.seq.Add(1))
			if err != nil {
				h.logger.Error("encode heartbeat", "err", err)
				continue
			}
			lastSent = time.Now()
			h.broadcast(clients, data)
		}
	}
}

func (h *Hub) broadcast(clients map[*client]struct{}, data []byte) {
	for c := range clients {
		select {
		case c.send <- data:
		default:
			h.logger.Warn("slow client, dropping frame", "addr", c.addr)
		}
	}
}

// ServeWS upgrades HTTP to WebSocket and starts per-client read/write loops.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("upgrade failed", "err", err, "addr", r.RemoteAddr)
		return
	}
	c := &client{
		conn: conn,
		send: make(chan []byte, sendBuffer),
		addr: r.RemoteAddr,
	}
	select {
	case h.register <- c:
	default:
		h.logger.Warn("register channel full, closing", "addr", c.addr)
		conn.Close()
		return
	}
	go h.writePump(c)
	go h.readPump(c)
}

func (h *Hub) writePump(c *client) {
	pingInterval := 30 * time.Second
	pingTicker := time.NewTicker(pingInterval)
	defer func() {
		pingTicker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case data, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-pingTicker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Hub) readPump(c *client) {
	defer func() {
		select {
		case h.unregister <- c:
		default:
		}
		c.conn.Close()
	}()
	c.conn.SetReadLimit(4096)
	_ = c.conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	})
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		h.handleClientMsg(c, msg)
	}
}

type clientMsg struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Action string `json:"action,omitempty"`
}

func (h *Hub) handleClientMsg(c *client, msg []byte) {
	var req clientMsg
	if err := json.Unmarshal(msg, &req); err != nil {
		h.sendAck(c, "", false, "bad json")
		return
	}
	switch req.Type {
	case "command":
		if !h.allowControl {
			h.sendAck(c, req.ID, false, "control disabled")
			return
		}
		cmd, ok := meter.ParseCommand(req.Action)
		if !ok {
			h.sendAck(c, req.ID, false, "unknown action")
			return
		}
		if !h.serial.Submit(cmd) {
			h.sendAck(c, req.ID, false, "command queue full")
			return
		}
		h.sendAck(c, req.ID, true, "")
	case "resync":
		select {
		case h.resync <- c:
		default:
		}
	default:
		h.sendAck(c, req.ID, false, "unknown type")
	}
}

func (h *Hub) sendAck(c *client, id string, ok bool, reason string) {
	frame := map[string]any{
		"type": "ack",
		"ref":  id,
		"ok":   ok,
	}
	if reason != "" {
		frame["reason"] = reason
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

func encodeTelemetry(s meter.Snapshot, seq uint64) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": "telemetry",
		"seq":  seq,
		"ts":   s.Timestamp.Format(time.RFC3339Nano),
		"data": s,
	})
}

func encodeHeartbeat(seq uint64) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": "heartbeat",
		"seq":  seq,
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}
