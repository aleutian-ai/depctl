package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// stream runs fn, relaying everything it writes to the client as NDJSON
// log lines while it happens, then one terminating result or error line.
// Sync and GC can run for minutes, so their output can't wait for the
// response to finish.
func stream(w http.ResponseWriter, fn func(out io.Writer) (any, error)) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	lines := newLineWriter(w)
	result, err := fn(lines)
	lines.flushPartial()

	if err != nil {
		lines.send(api.StreamLine{Error: err.Error(), Kind: errorKind(err)})
		return
	}
	body, err := json.Marshal(result)
	if err != nil {
		lines.send(api.StreamLine{Error: "encode result: " + err.Error()})
		return
	}
	lines.send(api.StreamLine{Result: body})
}

// lineWriter turns arbitrary writes into whole NDJSON log lines. Handler
// code keeps writing to a plain io.Writer, unchanged.
type lineWriter struct {
	mu      sync.Mutex
	enc     *json.Encoder
	flusher http.Flusher
	buf     bytes.Buffer
}

func newLineWriter(w http.ResponseWriter) *lineWriter {
	l := &lineWriter{enc: json.NewEncoder(w)}
	if f, ok := w.(http.Flusher); ok {
		l.flusher = f
		f.Flush()
	}
	return l
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// Not a whole line yet; keep it until more arrives.
			l.buf.Reset()
			l.buf.WriteString(line)
			break
		}
		l.sendLocked(api.StreamLine{Log: line[:len(line)-1]})
	}
	return len(p), nil
}

// flushPartial emits any trailing output that never ended in a newline.
func (l *lineWriter) flushPartial() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() == 0 {
		return
	}
	l.sendLocked(api.StreamLine{Log: l.buf.String()})
	l.buf.Reset()
}

func (l *lineWriter) send(line api.StreamLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sendLocked(line)
}

func (l *lineWriter) sendLocked(line api.StreamLine) {
	_ = l.enc.Encode(line)
	if l.flusher != nil {
		l.flusher.Flush()
	}
}
