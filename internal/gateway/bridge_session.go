package gateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"remote-codex/internal/auth"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/protocol"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const bridgeResumeGrace = 60 * time.Second

type bridgeFrame struct {
	sequence uint32
	data     []byte
}

// 未确认的消息在会话内保留；重连沿用原有 MessagePort 和 generation。
type bridgeJournal struct {
	mu                           sync.Mutex
	inputMu                      sync.Mutex
	frames                       []bridgeFrame
	bytes                        int
	sent, acknowledged, received uint32
	notify                       chan struct{}
}

func newBridgeJournal() *bridgeJournal { return &bridgeJournal{notify: make(chan struct{}, 1)} }

func (j *bridgeJournal) append(data []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(data) > protocol.MaxFrame || j.bytes+len(data)+4 > protocol.MaxQueue || len(j.frames) >= protocol.MaxQueuedFrames || j.sent == ^uint32(0) {
		return errors.New("QUEUE_LIMIT")
	}
	j.sent++
	frame := make([]byte, len(data)+4)
	binary.BigEndian.PutUint32(frame, j.sent)
	copy(frame[4:], data)
	j.frames = append(j.frames, bridgeFrame{j.sent, frame})
	j.bytes += len(frame)
	select {
	case j.notify <- struct{}{}:
	default:
	}
	return nil
}

func (j *bridgeJournal) ack(sequence uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if sequence > j.sent {
		return errors.New("ACK_INVALID")
	}
	if sequence <= j.acknowledged {
		return nil
	}
	count := 0
	for count < len(j.frames) && j.frames[count].sequence <= sequence {
		j.bytes -= len(j.frames[count].data)
		j.frames[count] = bridgeFrame{}
		count++
	}
	j.frames = j.frames[count:]
	j.acknowledged = sequence
	return nil
}

func (j *bridgeJournal) next(cursor uint32) *bridgeFrame {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, frame := range j.frames {
		if frame.sequence > cursor {
			return &frame
		}
	}
	return nil
}

func (j *bridgeJournal) receive(data []byte, forward func([]byte) error) (uint32, error) {
	j.inputMu.Lock()
	defer j.inputMu.Unlock()
	j.mu.Lock()
	received := j.received
	j.mu.Unlock()
	if len(data) < 5 || len(data) > protocol.MaxFrame+4 {
		return received, errors.New("FRAME_INVALID")
	}
	sequence := binary.BigEndian.Uint32(data)
	if sequence == 0 || sequence > received+1 {
		return received, errors.New("SEQUENCE_INVALID")
	}
	if sequence <= received {
		return received, nil
	}
	if err := forward(data[4:]); err != nil {
		return received, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.received = sequence
	return j.received, nil
}

func (g *Gateway) releaseClaim(item *claim) {
	item.mu.Lock()
	generation := item.generation
	item.mu.Unlock()
	if err := g.Controllers.Release(item.id, generation); err != nil {
		g.Log.Add(logger.Event{Event: "host_idle_failed", ErrorCode: "HOST_IDLE_FAILED", Generation: generation})
	}
	g.touchHostActivity()
	g.Auth.Unregister(item.session, item.id)
	g.mu.Lock()
	delete(g.claims, item.id)
	if g.active == item {
		g.active = nil
	}
	g.mu.Unlock()
}

func (g *Gateway) websocketSession(w http.ResponseWriter, r *http.Request, session *auth.Session, item *claim, client *host.Client) {
	ack64, err := strconv.ParseUint(r.URL.Query().Get("ack"), 10, 32)
	if err != nil {
		http.Error(w, "ACK_REQUIRED", 400)
		return
	}
	ack := uint32(ack64)
	upgrader := g.websocketUpgrader()
	upgrader.Subprotocols = []string{"cwb.v2"}
	var connection *websocket.Conn
	first := false
	err = g.Controllers.With(item.id, item.generation, func() error {
		item.mu.Lock()
		defer item.mu.Unlock()
		if item.revoked || item.journal == nil {
			return errors.New("CLAIM_ENDED")
		}
		if err := item.journal.ack(ack); err != nil {
			return err
		}
		var err error
		connection, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			return err
		}
		if item.socket != nil {
			item.socket.Close()
		}
		item.socket = connection
		if item.timer != nil {
			item.timer.Stop()
		}
		first = !item.activated
		item.activated = true
		return nil
	})
	if err != nil {
		if connection == nil {
			http.Error(w, "CLAIM_ENDED", 409)
		}
		return
	}
	g.touchHostActivity()
	done := make(chan struct{})
	defer func() {
		close(done)
		connection.Close()
		item.mu.Lock()
		if item.socket != connection {
			item.mu.Unlock()
			return
		}
		item.socket = nil
		if item.revoked {
			item.mu.Unlock()
			g.releaseClaim(item)
			return
		}
		item.timer = time.AfterFunc(bridgeResumeGrace, func() {
			item.mu.Lock()
			if item.socket != nil || item.revoked {
				item.mu.Unlock()
				return
			}
			item.revoked = true
			item.mu.Unlock()
			g.releaseClaim(item)
		})
		item.mu.Unlock()
		g.touchHostActivity()
	}()
	connection.SetReadLimit(protocol.MaxFrame + 4)
	connection.SetReadDeadline(time.Now().Add(90 * time.Second))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	connection.SetPingHandler(func(data string) error {
		return connection.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(15*time.Second))
	})
	writeControl := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return item.writeTo(connection, websocket.TextMessage, data)
	}
	item.journal.mu.Lock()
	received := item.journal.received
	item.journal.mu.Unlock()
	if writeControl(map[string]any{"kind": "ready", "received": received}) != nil {
		return
	}
	if first {
		err = g.Controllers.With(item.id, item.generation, func() error {
			if !g.Auth.Register(session.ID, item.id, func() { item.close(4001) }) {
				return errors.New("SESSION_ENDED")
			}
			g.mu.Lock()
			g.active = item
			g.mu.Unlock()
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			_, err := client.Call(ctx, map[string]any{"kind": "activate", "generation": item.generation})
			return err
		})
		if err != nil {
			item.close(4010)
			return
		}
	}
	g.Log.Add(logger.Event{Event: "browser_ws_connected", Generation: item.generation})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		cursor := ack
		for {
			item.mu.Lock()
			valid := !item.revoked && item.socket == connection
			item.mu.Unlock()
			if !valid {
				return
			}
			if frame := item.journal.next(cursor); frame != nil {
				begin := time.Now()
				if err := item.writeTo(connection, websocket.BinaryMessage, frame.data); err != nil {
					item.mu.Lock()
					valid = !item.revoked && item.socket == connection
					item.mu.Unlock()
					if valid && !errors.Is(err, net.ErrClosed) && !errors.Is(err, websocket.ErrCloseSent) {
						code := "WS_DATA_WRITE_FAILED"
						var network net.Error
						if errors.As(err, &network) && network.Timeout() {
							code = "WS_DATA_WRITE_TIMEOUT"
						}
						g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: code, DurationMS: time.Since(begin).Milliseconds(), Generation: item.generation})
					}
					connection.Close()
					return
				}
				cursor = frame.sequence
				continue
			}
			select {
			case <-done:
				return
			case <-item.journal.notify:
			case <-ticker.C:
				item.mu.Lock()
				valid = !item.revoked && item.socket == connection
				item.mu.Unlock()
				if !valid {
					return
				}
				if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(15*time.Second)); err != nil {
					item.mu.Lock()
					valid = !item.revoked && item.socket == connection
					item.mu.Unlock()
					select {
					case <-done:
						valid = false
					default:
					}
					if valid && !errors.Is(err, net.ErrClosed) && !errors.Is(err, websocket.ErrCloseSent) {
						code := "WS_PING_FAILED"
						var network net.Error
						if errors.As(err, &network) && network.Timeout() {
							code = "WS_PING_TIMEOUT"
						}
						g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: code, Generation: item.generation})
					}
					connection.Close()
					return
				}
			}
		}
	}()
	for {
		kind, data, err := connection.ReadMessage()
		if err != nil {
			return
		}
		g.touchHostActivity()
		connection.SetReadDeadline(time.Now().Add(90 * time.Second))
		item.mu.Lock()
		valid := !item.revoked && item.socket == connection
		item.mu.Unlock()
		if !valid {
			return
		}
		if kind == websocket.TextMessage {
			var control struct {
				Kind     string `json:"kind"`
				Sequence uint32 `json:"sequence"`
			}
			if json.Unmarshal(data, &control) != nil {
				item.close(4008)
				return
			}
			switch control.Kind {
			case "ack":
				if item.journal.ack(control.Sequence) != nil {
					item.close(4008)
					return
				}
			case "probe":
				if writeControl(map[string]string{"kind": "pong"}) != nil {
					return
				}
			default:
				item.close(4008)
				return
			}
		} else if kind == websocket.BinaryMessage {
			sequence, err := item.journal.receive(data, func(payload []byte) error {
				return g.Controllers.With(item.id, item.generation, func() error { return client.Write(websocket.BinaryMessage, payload) })
			})
			if err != nil {
				item.close(4008)
				return
			}
			if writeControl(map[string]any{"kind": "ack", "sequence": sequence}) != nil {
				return
			}
		} else {
			item.close(4008)
			return
		}
	}
}
