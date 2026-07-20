package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/mrn-dk/mortise/internal/dedupe"
	"github.com/mrn-dk/mortise/internal/openai"
)

// relayBody streams src to the client, flushing for SSE, and returns the full
// bytes seen (for token accounting and dedup capture). clientErr is non-nil if
// writing to the client failed (e.g. disconnect) — in which case the captured
// body is incomplete and must not be cached.
func relayBody(w http.ResponseWriter, src io.Reader) (body []byte, clientErr error) {
	flusher, _ := w.(http.Flusher)
	var buf bytes.Buffer
	r := bufio.NewReader(src)
	chunk := make([]byte, 32<<10)
	for {
		n, rerr := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if _, werr := w.Write(chunk[:n]); werr != nil {
				return buf.Bytes(), werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Upstream read error: client got a partial body. Return the read
			// error as clientErr so the response is not cached for replay.
			return buf.Bytes(), rerr
		}
	}
	return buf.Bytes(), nil
}

// replay writes a previously captured response to a duplicate request's client.
func replay(w http.ResponseWriter, res *dedupe.Result) {
	copyHeader(w.Header(), res.Header)
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// extractUsage pulls token usage from a response body. For non-streaming
// responses it reads the top-level usage object; for SSE it scans data frames
// for the last one carrying a non-null usage (requires stream_options.include_usage).
func extractUsage(body []byte, stream bool) *openai.Usage {
	if len(body) == 0 {
		return nil
	}
	if !stream {
		var cr openai.ChatResponse
		if err := json.Unmarshal(body, &cr); err != nil {
			return nil
		}
		return cr.Usage
	}
	var last *openai.Usage
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var frame struct {
			Usage *openai.Usage `json:"usage"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			continue
		}
		if frame.Usage != nil {
			last = frame.Usage
		}
	}
	return last
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		if hopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	copyHeader(dst, src)
	return dst
}

// hopByHop reports headers that must not be forwarded verbatim.
func hopByHop(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length":
		return true
	}
	return false
}
