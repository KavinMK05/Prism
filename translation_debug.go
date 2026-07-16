package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// translationDebugSeq orders per-request dump directories so the most recent
// translation is easy to find.
var translationDebugSeq uint64

// translationDebugCap is the maximum number of per-request dump directories
// retained per endpoint. Oldest (lowest sequence number) are pruned first.
const translationDebugCap = 50

// translationDebugCapture records the four artifacts of a single translation
// hop (Responses API or Anthropic Messages API -> Ollama) for offline debugging:
//
//	1_original_request.json     - the incoming client request
//	2_translated_request.json   - the translated Ollama chat request
//	3_original_response.*       - raw bytes returned by Ollama (.json / .ndjson)
//	4_translated_response.*     - bytes sent back to the client (.json / .sse)
//
// Each request gets its own directory under
// <logdir>/debug/<endpoint>/<seq>_<model>_<mode>. Dumping is always on; only
// the most recent translationDebugCap directories per endpoint are kept.
type translationDebugCapture struct {
	endpoint string
	dir      string
	stream   bool

	bodyTee *strings.Builder // teed upstream response bytes (#3)
	outTee  *strings.Builder // teed client-bound bytes   (#4)
}

func newTranslationDebugCapture(endpoint string, stream bool, model string) *translationDebugCapture {
	seq := atomic.AddUint64(&translationDebugSeq, 1)
	safe := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_").Replace(model)
	if safe == "" {
		safe = "unknown"
	}
	mode := "nonstream"
	if stream {
		mode = "stream"
	}
	dir := filepath.Join(getLogDir(), "debug", endpoint, fmt.Sprintf("%06d_%s_%s", seq, safe, mode))
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[DEBUG] %s: failed to create debug dir: %v", endpoint, err)
		return nil
	}
	c := &translationDebugCapture{endpoint: endpoint, dir: dir, stream: stream, outTee: &strings.Builder{}}
	log.Printf("[DEBUG] %s translation dump -> %s", endpoint, dir)
	pruneTranslationDebugDirs(filepath.Dir(dir))
	return c
}

// pruneTranslationDebugDirs keeps only the most recent translationDebugCap
// directories in parent (oldest by modification time first). Sorting by
// modtime (rather than the zero-padded sequence number in the name) is robust
// to process restarts, which reset the sequence counter and would otherwise
// make fresh low-numbered dirs sort below stale high-numbered ones and get
// pruned immediately.
func pruneTranslationDebugDirs(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	type dirInfo struct {
		name    string
		modTime int64
	}
	var dirs []dirInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{name: e.Name(), modTime: info.ModTime().UnixNano()})
	}
	if len(dirs) <= translationDebugCap {
		return
	}
	// Newest first (highest modTime).
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].modTime > dirs[j].modTime })
	for _, d := range dirs[translationDebugCap:] {
		os.RemoveAll(filepath.Join(parent, d.name))
	}
}

// writeJSON pretty-prints v to <dir>/<name>.
func (c *translationDebugCapture) writeJSON(name string, v interface{}) {
	if c == nil {
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("[DEBUG] %s: marshal %s failed: %v", c.endpoint, name, err)
		return
	}
	c.writeRaw(name, b)
}

func (c *translationDebugCapture) writeRaw(name string, b []byte) {
	if c == nil {
		return
	}
	if err := os.WriteFile(filepath.Join(c.dir, name), b, 0644); err != nil {
		log.Printf("[DEBUG] %s: write %s failed: %v", c.endpoint, name, err)
	}
}

// teeBody returns a reader that mirrors r into a buffer so the raw upstream
// response (#3) can be flushed by finish().
func (c *translationDebugCapture) teeBody(r io.Reader) io.Reader {
	if c == nil {
		return r
	}
	if c.bodyTee == nil {
		c.bodyTee = &strings.Builder{}
	}
	return io.TeeReader(r, c.bodyTee)
}

// translationDebugWriter wraps the client ResponseWriter, copying every written
// byte into outTee so the translated response (#4) can be flushed by finish().
// Implements http.ResponseWriter and http.Flusher so it is transparent to
// callers that cast the writer (e.g. to obtain a flusher for SSE).
type translationDebugWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	buf     *strings.Builder
}

func (d *translationDebugWriter) Write(p []byte) (int, error) {
	if d.buf != nil {
		d.buf.Write(p)
	}
	return d.ResponseWriter.Write(p)
}

func (d *translationDebugWriter) Flush() {
	if d.flusher != nil {
		d.flusher.Flush()
	}
}

// wrapWriter wraps w for capture. Returns w unchanged when the capture is nil
// (only happens if the debug directory could not be created).
func (c *translationDebugCapture) wrapWriter(w http.ResponseWriter) http.ResponseWriter {
	if c == nil {
		return w
	}
	flusher, _ := w.(http.Flusher)
	return &translationDebugWriter{ResponseWriter: w, flusher: flusher, buf: c.outTee}
}

// finish flushes the buffered #3 (upstream response) and #4 (client response)
// artifacts to disk. Safe to defer unconditionally.
func (c *translationDebugCapture) finish() {
	if c == nil {
		return
	}
	if c.bodyTee != nil {
		name := "3_original_response.json"
		if c.stream {
			name = "3_original_response.ndjson"
		}
		c.writeRaw(name, []byte(c.bodyTee.String()))
	}
	if c.outTee != nil {
		ext := "json"
		if c.stream {
			ext = "sse"
		}
		c.writeRaw("4_translated_response."+ext, []byte(c.outTee.String()))
	}
}