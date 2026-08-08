package web

import (
	"bytes"
	"net/http"
	"time"

	"github.com/kiineld/telemt-panel/internal/store"
)

// getEvents streams re-rendered proxy rows whenever the poller completes a
// sweep. One poll loop serves every tab: this handler only subscribes to and
// reads the poller's cache, so tab count never affects load on telemt.
func (s *server) getEvents(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	updates, cancel := s.Poller.Subscribe()
	defer cancel()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	send := func() bool {
		rows, err := s.buildRows(r)
		if err != nil {
			return true
		}
		var buf bytes.Buffer
		if err := s.tmpl["proxies.html"].ExecuteTemplate(&buf, "rows",
			page{Rows: rows, Host: s.host()}); err != nil {
			return true
		}
		if _, err := w.Write(sseFrame("rows", buf.Bytes())); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-updates:
			if !open {
				return
			}
			if !send() {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// sseFrame formats one named SSE event. Every newline in the payload has to
// start its own data: line, or the frame is truncated at the first break —
// the payload here is a chunk of rendered HTML, which is full of newlines.
func sseFrame(event string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	for _, line := range bytes.Split(payload, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}
