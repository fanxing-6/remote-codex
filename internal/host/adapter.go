package host

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"remote-codex/internal/auth"
	"remote-codex/internal/protocol"
	"strconv"
	"sync"
	"time"
)

type ProcessIdentity struct {
	PID            int     `json:"pid"`
	StartedAt      float64 `json:"startedAt"`
	ExecutablePath string  `json:"executablePath"`
	AppVersion     string  `json:"appVersion"`
	AsarPath       string  `json:"asarPath"`
}
type AppCandidate struct {
	Platform        string
	Arch            string
	ProductID       string
	AppVersion      string
	ExecutablePath  string
	AsarPath        string
	AssetBuildID    string
	SignatureStatus string
}
type ShimEndpoint struct {
	ProcessIdentity
	URL                string  `json:"url"`
	IPCToken           string  `json:"-"`
	ShimInstanceID     string  `json:"shimInstanceId"`
	ProtocolVersion    int     `json:"protocolVersion"`
	AssetBuildID       string  `json:"assetBuildId"`
	InspectorClosed    bool    `json:"inspectorClosed"`
	InspectorOpenMS    float64 `json:"inspectorOpenMs"`
	BootstrapElapsedMS float64 `json:"bootstrapElapsedMs"`
}
type IHostAdapter interface {
	BootstrapperName() string
	DiscoverCandidates(context.Context) ([]AppCandidate, error)
	FindRunningProcess(context.Context) (ProcessIdentity, error)
	Attach(context.Context, ProcessIdentity, string, string) (*Client, error)
	Detach(context.Context, *Client) error
	ProbeRuntime(context.Context, *Client) error
}
type reply struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Value     json.RawMessage `json:"value"`
	ErrorCode string          `json:"errorCode"`
}
type Client struct {
	Endpoint   ShimEndpoint
	connection *websocket.Conn
	writeMu    sync.Mutex
	mu         sync.Mutex
	pending    map[string]chan json.RawMessage
	snapshot   map[string]any
	closed     chan struct{}
	onBinary   func([]byte)
	onOffline  func()
	stopping   bool
}

func Connect(endpoint ShimEndpoint) (*Client, error) {
	header := http.Header{"Authorization": []string{"Bearer " + endpoint.IPCToken}}
	connection, _, err := websocket.DefaultDialer.Dial(endpoint.URL, header)
	if err != nil {
		return nil, errors.New("IPC_CONNECT_FAILED")
	}
	connection.SetReadLimit(protocol.MaxFrame)
	c := &Client{Endpoint: endpoint, connection: connection, pending: map[string]chan json.RawMessage{}, closed: make(chan struct{})}
	go c.read()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	identity, err := c.Call(ctx, map[string]any{"kind": "hello", "pid": endpoint.PID, "startedAt": endpoint.StartedAt, "shimInstanceId": endpoint.ShimInstanceID, "protocolVersion": protocol.Version, "assetBuildId": endpoint.AssetBuildID})
	if err != nil {
		connection.Close()
		return nil, err
	}
	if identity["pid"] != float64(endpoint.PID) || identity["startedAt"] != endpoint.StartedAt || identity["shimInstanceId"] != endpoint.ShimInstanceID || identity["protocolVersion"] != float64(protocol.Version) || identity["assetBuildId"] != endpoint.AssetBuildID {
		connection.Close()
		return nil, errors.New("IPC_IDENTITY_MISMATCH")
	}
	return c, nil
}
func (c *Client) read() {
	defer func() {
		c.connection.Close()
		close(c.closed)
		c.mu.Lock()
		offline := c.onOffline
		stopping := c.stopping
		c.mu.Unlock()
		if offline != nil && !stopping {
			offline()
		}
	}()
	for {
		kind, data, err := c.connection.ReadMessage()
		if err != nil {
			code := "IPC_READ_FAILED"
			var closed *websocket.CloseError
			var network net.Error
			if errors.As(err, &closed) {
				code = "IPC_CLOSE_" + strconv.Itoa(closed.Code)
			} else if errors.As(err, &network) && network.Timeout() {
				code = "IPC_TIMEOUT"
			} else if errors.Is(err, websocket.ErrReadLimit) {
				code = "FRAME_LIMIT"
			}
			c.failure(code)
			return
		}
		if kind == websocket.BinaryMessage {
			c.mu.Lock()
			handler := c.onBinary
			c.mu.Unlock()
			if handler != nil {
				handler(data)
			}
			continue
		}
		var message reply
		if json.Unmarshal(data, &message) != nil {
			c.failure("IPC_CONTROL_INVALID")
			return
		}
		if message.Kind == "host-offline" {
			c.mu.Lock()
			if c.snapshot == nil {
				c.snapshot = map[string]any{}
			}
			c.snapshot["ready"] = false
			c.snapshot["errorCode"] = message.ErrorCode
			c.mu.Unlock()
			return
		}
		if message.Kind != "reply" {
			c.failure("IPC_CONTROL_UNKNOWN")
			return
		}
		c.mu.Lock()
		waiter := c.pending[message.ID]
		c.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- message.Value:
			default:
			}
		}
	}
}
func (c *Client) failure(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshot == nil {
		c.snapshot = map[string]any{}
	}
	c.snapshot["errorCode"] = code
	c.snapshot["ready"] = false
}
func (c *Client) Call(ctx context.Context, message map[string]any) (map[string]any, error) {
	if message["kind"] == "stop" {
		c.mu.Lock()
		c.stopping = true
		c.mu.Unlock()
	}
	id := auth.Random()
	message["id"] = id
	waiter := make(chan json.RawMessage, 1)
	c.mu.Lock()
	if len(c.pending) >= protocol.MaxPending {
		c.mu.Unlock()
		return nil, errors.New("IPC_PENDING_LIMIT")
	}
	c.pending[id] = waiter
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if err = c.Write(websocket.TextMessage, data); err != nil {
		return nil, err
	}
	var response json.RawMessage
	select {
	case response = <-waiter:
	case <-c.closed:
		select {
		case response = <-waiter:
		default:
			return nil, errors.New("HOST_OFFLINE")
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var result map[string]any
	if err = json.Unmarshal(response, &result); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if _, ok := result["ready"]; ok {
		c.snapshot = result
	}
	c.mu.Unlock()
	return result, nil
}
func (c *Client) Write(kind int, data []byte) error {
	if len(data) > protocol.MaxFrame {
		return errors.New("FRAME_LIMIT")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.connection.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return c.connection.WriteMessage(kind, data)
}
func (c *Client) Reset(generation uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := c.Call(ctx, map[string]any{"kind": "reset", "generation": generation})
	return err
}
func (c *Client) Idle(generation uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Call(ctx, map[string]any{"kind": "idle", "generation": generation})
	return err
}
func (c *Client) Handlers(binary func([]byte), offline func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onBinary = binary
	c.onOffline = offline
}
func (c *Client) Snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := map[string]any{}
	for key, value := range c.snapshot {
		result[key] = value
	}
	return result
}
func (c *Client) Close() error { return c.connection.Close() }
