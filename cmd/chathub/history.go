package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MaxHistoryLines is the max number of messages restored per channel on startup.
const MaxHistoryLines = 200

// HistoryWriter persists chat messages to per-channel log files.
type HistoryWriter struct {
	mu     sync.Mutex
	files  map[string]*os.File // channel -> open file
	dir    string
}

// NewHistoryWriter creates a writer with logs at <configDir>/logs/<channel>.jsonl.
func NewHistoryWriter() *HistoryWriter {
	dir := filepath.Join(filepath.Dir(hubConfigPath()), "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[HISTORY] mkdir failed: %v", err)
	}
	return &HistoryWriter{files: make(map[string]*os.File), dir: dir}
}

// safeFilename maps a channel key to a filesystem-legal filename. Twitch
// channels like "miwitv" pass through unchanged; YouTube tab keys like
// "yt:@DEmiwitv" contain a colon which is reserved on Windows (NTFS
// streams), so the file open silently fails and Append/Load drop
// everything on the floor. Replace the small set of reserved chars
// (Windows is the strict one — Mac/Linux only object to "/") with "_".
func safeFilename(channel string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		return r
	}, channel)
}

// Append writes a chat message JSON line for the given channel.
func (hw *HistoryWriter) Append(channel string, msg map[string]interface{}) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	f, ok := hw.files[channel]
	if !ok {
		path := filepath.Join(hw.dir, safeFilename(channel)+".jsonl")
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("[HISTORY] open %s failed: %v", path, err)
			return
		}
		hw.files[channel] = f
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("[HISTORY] write %s failed: %v", channel, err)
	}
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

// Load reads the last N messages from a channel's history file.
func (hw *HistoryWriter) Load(channel string, limit int) []map[string]interface{} {
	path := filepath.Join(hw.dir, safeFilename(channel)+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		log.Printf("[HISTORY] load %s: %v", path, err)
		return nil
	}
	defer f.Close()

	// Read all lines (file is reasonably small per channel)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	// Take last N
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	out := make([]map[string]interface{}, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			continue
		}
		// Flag as historical so frontend can style differently
		m["historical"] = true
		out = append(out, m)
	}
	log.Printf("[HISTORY] load %s: %d lines returned (file had %d)", path, len(out), len(lines))
	return out
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
	hw.mu.Unlock()
	return os.Remove(filepath.Join(hw.dir, safeFilename(channel)+".jsonl"))
}

// HistoryDir returns the directory where logs are stored.
func (hw *HistoryWriter) HistoryDir() string {
	return hw.dir
}

// ensureValidChannelName guards against path traversal in channel names.
func ensureValidChannelName(channel string) bool {
	if channel == "" || strings.ContainsAny(channel, "/\\:.") {
		return false
	}
	return true
}

// Verify compiles
var _ = fmt.Sprintf
