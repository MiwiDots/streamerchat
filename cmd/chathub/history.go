package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/miwi/streamerchat/internal/fsutil"
)

// MaxHistoryLines is the max number of messages restored per channel on startup.
const MaxHistoryLines = 200

// History file sizing.
//
// Files used to grow forever and Load read the whole file to return the
// last 200 lines. Now Load only reads the tail, and a file is compacted
// (rewritten atomically with only its newest lines) once it passes
// historyCompactBytes.
const (
	historyCompactBytes = 4 << 20 // compact when a file grows past 4 MB
	historyKeepLines    = 2000    // lines kept by a compaction
	historyTailBytes    = 1 << 20 // bytes read from the end by Load
)

// historyPersistTypes are the message types written to disk. Chat-like
// messages are restored on startup; the moderation types are tombstones —
// never restored themselves, but applied on Load so messages deleted,
// banned or cleared before a restart don't come back.
var historyPersistTypes = map[string]bool{
	"chat": true, "sub": true, "giftsub": true, "raid": true, "announcement": true, "watchstreak": true,
	"deleted": true, "ban": true, "timeout": true, "clearchat": true,
}

func isTombstone(t string) bool {
	return t == "deleted" || t == "ban" || t == "timeout" || t == "clearchat"
}

// HistoryWriter persists chat messages to per-channel log files.
type HistoryWriter struct {
	mu    sync.Mutex
	files map[string]*os.File // channel -> open file
	sizes map[string]int64    // channel -> current file size
	dir   string
}

// NewHistoryWriter creates a writer with logs at <configDir>/logs/<channel>.jsonl.
func NewHistoryWriter() *HistoryWriter {
	return newHistoryWriterAt(filepath.Join(filepath.Dir(hubConfigPath()), "logs"))
}

func newHistoryWriterAt(dir string) *HistoryWriter {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[HISTORY] mkdir failed: %v", err)
	}
	return &HistoryWriter{files: make(map[string]*os.File), sizes: make(map[string]int64), dir: dir}
}

// safeFilename maps a channel key to a filesystem-legal filename. Twitch
// channels like "miwitv" pass through unchanged; YouTube tab keys like
// "yt:@DEmiwitv" contain a colon which is reserved on Windows (NTFS
// streams), so the file open silently fails and Append/Load drop
// everything on the floor. Replace the small set of reserved chars
// (Windows is the strict one — Mac/Linux only object to "/") with "_".
// Path separators and ".." can't survive this, so it also prevents path
// traversal out of the log directory.
func safeFilename(channel string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, channel)
	if name == "" || name == "." || name == ".." {
		name = "_" + name
	}
	return name
}

func (hw *HistoryWriter) path(channel string) string {
	return filepath.Join(hw.dir, safeFilename(channel)+".jsonl")
}

// Append writes a message as one JSON line if its type is persisted.
func (hw *HistoryWriter) Append(channel string, msg map[string]interface{}) {
	if t, _ := msg["type"].(string); !historyPersistTypes[t] {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	hw.mu.Lock()
	defer hw.mu.Unlock()

	f, err := hw.openLocked(channel)
	if err != nil {
		log.Printf("[HISTORY] open %s failed: %v", channel, err)
		return
	}
	n, err := f.Write(append(data, '\n'))
	hw.sizes[channel] += int64(n)
	if err != nil {
		log.Printf("[HISTORY] write %s failed: %v", channel, err)
		return
	}
	if hw.sizes[channel] > historyCompactBytes {
		if err := hw.compactLocked(channel); err != nil {
			log.Printf("[HISTORY] compact %s failed: %v", channel, err)
		}
	}
}

func (hw *HistoryWriter) openLocked(channel string) (*os.File, error) {
	if f, ok := hw.files[channel]; ok {
		return f, nil
	}
	f, err := os.OpenFile(hw.path(channel), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil {
		hw.sizes[channel] = st.Size()
	}
	hw.files[channel] = f
	return f, nil
}

// compactLocked rewrites the channel's file with only its newest
// historyKeepLines lines (atomic replace), then reopens it for appending.
func (hw *HistoryWriter) compactLocked(channel string) error {
	if f, ok := hw.files[channel]; ok {
		f.Close()
		delete(hw.files, channel)
	}
	path := hw.path(channel)
	// Generous read window: 2000 lines of chat JSON fit well within 4 MB.
	lines, err := readTailLines(path, historyCompactBytes)
	if err != nil {
		return err
	}
	if len(lines) > historyKeepLines {
		lines = lines[len(lines)-historyKeepLines:]
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := fsutil.WriteFileAtomic(path, buf.Bytes(), 0644); err != nil {
		return err
	}
	_, err = hw.openLocked(channel)
	log.Printf("[HISTORY] compacted %s to %d lines", channel, len(lines))
	return err
}

// readTailLines returns the complete lines contained in the last maxBytes
// of the file. A line cut off at the start of the window is dropped.
func readTailLines(path string, maxBytes int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := st.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	if start > 0 && !startsAtLineBoundary(f, start) {
		// The window begins mid-line: drop that partial first line.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	var lines [][]byte
	for _, l := range bytes.Split(buf, []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// startsAtLineBoundary reports whether offset is the first byte of a line,
// i.e. the byte before it is a newline.
func startsAtLineBoundary(f *os.File, offset int64) bool {
	prev := make([]byte, 1)
	_, err := f.ReadAt(prev, offset-1)
	return err == nil && prev[0] == '\n'
}

// Close closes all open history files.
func (hw *HistoryWriter) Close() {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	for _, f := range hw.files {
		f.Close()
	}
	hw.files = make(map[string]*os.File)
}

// Load returns up to limit of the newest restorable messages for a
// channel, with tombstones applied: a "deleted" line removes the message
// with that id, "ban"/"timeout" remove that user's earlier messages, and
// "clearchat" removes everything before it.
func (hw *HistoryWriter) Load(channel string, limit int) []map[string]interface{} {
	path := hw.path(channel)
	lines, err := readTailLines(path, historyTailBytes)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[HISTORY] load %s: %v", path, err)
		}
		return nil
	}
	out := applyTombstones(lines)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	for _, m := range out {
		// Flag as historical so frontend can style differently
		m["historical"] = true
	}
	log.Printf("[HISTORY] load %s: %d messages returned (%d lines read)", path, len(out), len(lines))
	return out
}

// applyTombstones parses lines in order and returns the surviving
// restorable messages.
func applyTombstones(lines [][]byte) []map[string]interface{} {
	var out []map[string]interface{}
	for _, l := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal(l, &m); err != nil {
			continue
		}
		t, _ := m["type"].(string)
		if !isTombstone(t) {
			out = append(out, m)
			continue
		}
		switch t {
		case "clearchat":
			out = out[:0]
		case "deleted":
			if id, _ := m["id"].(string); id != "" {
				out = filterMessages(out, func(x map[string]interface{}) bool {
					xid, _ := x["id"].(string)
					return xid == id
				})
			}
		case "ban", "timeout":
			if user, _ := m["targetUsername"].(string); user != "" {
				out = filterMessages(out, func(x map[string]interface{}) bool {
					xu, _ := x["username"].(string)
					return strings.EqualFold(xu, user)
				})
			}
		}
	}
	return out
}

// filterMessages removes (in place) every message for which drop is true.
func filterMessages(ms []map[string]interface{}, drop func(map[string]interface{}) bool) []map[string]interface{} {
	kept := ms[:0]
	for _, m := range ms {
		if !drop(m) {
			kept = append(kept, m)
		}
	}
	return kept
}

// Channels returns all channels that have history files.
func (hw *HistoryWriter) Channels() []string {
	entries, err := os.ReadDir(hw.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	return out
}

// Clear deletes the history for a channel.
func (hw *HistoryWriter) Clear(channel string) error {
	hw.mu.Lock()
	if f, ok := hw.files[channel]; ok {
		f.Close()
		delete(hw.files, channel)
	}
	delete(hw.sizes, channel)
	hw.mu.Unlock()
	return os.Remove(hw.path(channel))
}

// HistoryDir returns the directory where logs are stored.
func (hw *HistoryWriter) HistoryDir() string {
	return hw.dir
}
