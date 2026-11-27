package logger

import (
	"encoding/json"
	"io"
	stdlog "log"
	"sync"
	"time"
)

type Event struct {
	Time       time.Time `json:"time"`
	Event      string    `json:"event"`
	Status     int       `json:"status,omitempty"`
	DurationMS int64     `json:"durationMs,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	ErrorCode  string    `json:"errorCode,omitempty"`
}
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	events []Event
}

func New(out io.Writer) *Logger { return &Logger{out: out} }
func (l *Logger) Add(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Time = time.Now().UTC()
	l.events = append(l.events, event)
	if len(l.events) > 300 {
		l.events = l.events[1:]
	}
	if err := json.NewEncoder(l.out).Encode(event); err != nil {
		panic(err)
	}
}
func (l *Logger) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event{}, l.events...)
}

type httpErrorWriter struct{ logger *Logger }

func (w httpErrorWriter) Write(data []byte) (int, error) {
	w.logger.Add(Event{Event: "http_server_error", ErrorCode: "HTTP_SERVER_ERROR"})
	return len(data), nil
}
func (l *Logger) HTTPErrorLog() *stdlog.Logger { return stdlog.New(httpErrorWriter{l}, "", 0) }
