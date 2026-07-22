package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/mrn-dk/mortise/internal/dedupe"
	"github.com/mrn-dk/mortise/internal/openai"
)

// relayResult reports the outcome of streaming an upstream body to the client.
type relayResult struct {
	// usage is the parsed token accounting, if the response carried any.
	usage *openai.Usage
	// cacheBody holds the full response for idempotent replay. It is non-nil
	// only when capture was requested, the body fit within maxCache, and the
	// client received it in full.
	cacheBody []byte
	// clientErr is non-nil if writing to the client failed (disconnect) or the
	// upstream read errored mid-body — in which case the response is partial
	// and must not be cached.
	clientErr error
}

// relay streams src to the client, flushing for SSE. It extracts token usage
// and — when capture is true and the body stays within maxCache — retains the
// full body for idempotent replay.
//
// Memory: for streaming responses usage is parsed incrementally and nothing is
// retained unless capture is set, so an unbounded SSE stream does not grow the
// heap. Non-streaming bodies are buffered (bounded by the completion size) so
// their top-level usage object can be parsed.
func relay(w http.ResponseWriter, src io.Reader, stream, capture bool, maxCache int) relayResult {
	flusher, _ := w.(http.Flusher)

	var buf bytes.Buffer     // holds bytes we retain (capture and/or usage)
	var sse *sseUsageScanner // incremental usage parser for streams
	buffering := capture || !stream
	if stream {
		sse = &sseUsageScanner{}
	}

	res := relayResult{}
	capReached := false
	chunk := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(chunk)
		if n > 0 {
			p := chunk[:n]
			if buffering {
				buf.Write(p)
				if capture && !capReached && buf.Len() > maxCache {
					capReached = true // too big to cache
					if stream {
						// Usage comes from the incremental scanner; stop
						// retaining the (now uncacheable) stream body.
						buffering = false
						buf.Reset()
					}
				}
			}
			if stream {
				sse.write(p)
			}
			if _, werr := w.Write(p); werr != nil {
				res.clientErr = werr
				return res
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Partial body delivered; surface as clientErr so it is not cached.
			res.clientErr = rerr
			return res
		}
	}

	if stream {
		res.usage = sse.usage()
	} else {
		res.usage = parseUsage(buf.Bytes())
	}
	if capture && !capReached {
		res.cacheBody = buf.Bytes()
	}
	return res
}

// replay writes a previously captured response to a duplicate request's client.
func replay(w http.ResponseWriter, res *dedupe.Result) {
	copyHeader(w.Header(), res.Header)
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// parseUsage reads the top-level usage object from a non-streaming response.
func parseUsage(body []byte) *openai.Usage {
	if len(body) == 0 {
		return nil
	}
	var cr openai.ChatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil
	}
	return cr.Usage
}

// sseUsageScanner extracts the last non-null usage object from an SSE stream,
// tolerating chunk boundaries that split lines. It retains only the current
// partial line plus the most recent usage — not the whole stream.
type sseUsageScanner struct {
	line bytes.Buffer
	last *openai.Usage
}

func (s *sseUsageScanner) write(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.line.Write(p)
			return
		}
		s.line.Write(p[:i])
		s.processLine(s.line.Bytes())
		s.line.Reset()
		p = p[i+1:]
	}
}

func (s *sseUsageScanner) processLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var frame struct {
		Usage *openai.Usage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &frame); err == nil && frame.Usage != nil {
		s.last = frame.Usage
	}
}

func (s *sseUsageScanner) usage() *openai.Usage {
	if s.line.Len() > 0 {
		s.processLine(s.line.Bytes())
		s.line.Reset()
	}
	return s.last
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
