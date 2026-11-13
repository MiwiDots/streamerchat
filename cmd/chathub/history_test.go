package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chatMsg(id, user, text string) map[string]interface{} {
	return map[string]interface{}{"type": "chat", "id": id, "username": user, "text": text}
}

func texts(ms []map[string]interface{}) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m["text"].(string))
	}
	return out
}

func TestHistoryTombstonesSurviveRestart(t *testing.T) {
	hw := newHistoryWriterAt(t.TempDir())
	ch := "miwitv"
	hw.Append(ch, chatMsg("1", "alice", "hi"))
	hw.Append(ch, chatMsg("2", "troll", "slur"))
	hw.Append(ch, chatMsg("3", "bob", "deleted by mod"))
	hw.Append(ch, map[string]interface{}{"type": "deleted", "id": "3", "targetUsername": "bob"})
	hw.Append(ch, map[string]interface{}{"type": "ban", "targetUsername": "TROLL"})
	hw.Append(ch, chatMsg("4", "troll", "after unban")) // later messages stay
	hw.Append(ch, map[string]interface{}{"type": "system", "text": "noise"}) // not persisted
	hw.Close()

	got := texts(hw.Load(ch, 200))
	want := []string{"hi", "after unban"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHistoryClearChatDropsEarlier(t *testing.T) {
	hw := newHistoryWriterAt(t.TempDir())
	hw.Append("c", chatMsg("1", "a", "before"))
	hw.Append("c", map[string]interface{}{"type": "clearchat"})
	hw.Append("c", chatMsg("2", "a", "after"))
	if got := texts(hw.Load("c", 200)); len(got) != 1 || got[0] != "after" {
		t.Fatalf("got %v", got)
	}
}

func TestHistoryLoadLimitAndHistoricalFlag(t *testing.T) {
	hw := newHistoryWriterAt(t.TempDir())
	for i := 0; i < 10; i++ {
		hw.Append("c", chatMsg(fmt.Sprint(i), "a", fmt.Sprint(i)))
	}
	got := hw.Load("c", 3)
	if strings.Join(texts(got), ",") != "7,8,9" {
		t.Fatalf("got %v", texts(got))
	}
	if got[0]["historical"] != true {
		t.Fatal("historical flag missing")
	}
}

func TestHistoryCompaction(t *testing.T) {
	dir := t.TempDir()
	hw := newHistoryWriterAt(dir)
	pad := strings.Repeat("x", 3000) // ~3 KB per line → compaction after ~1400 lines
	total := historyCompactBytes/3000 + 200
	for i := 0; i < total; i++ {
		hw.Append("c", chatMsg(fmt.Sprint(i), "a", fmt.Sprintf("%d %s", i, pad)))
	}
	hw.Close()
	st, err := os.Stat(filepath.Join(dir, "c.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > historyCompactBytes {
		t.Fatalf("file not compacted: %d bytes", st.Size())
	}
	got := hw.Load("c", 1)
	if !strings.HasPrefix(got[0]["text"].(string), fmt.Sprintf("%d ", total-1)) {
		t.Fatal("newest message lost by compaction")
	}
}

func TestReadTailLinesDropsPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	if err := os.WriteFile(path, []byte("aaaa\nbbbb\ncccc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	lines, err := readTailLines(path, 7) // window "bb\ncccc\n" starts mid-line
	if err != nil || len(lines) != 1 || string(lines[0]) != "cccc" {
		t.Fatalf("got %q, %v", lines, err)
	}
	lines, _ = readTailLines(path, 10) // window starts exactly at "bbbb"
	if len(lines) != 2 || string(lines[0]) != "bbbb" {
		t.Fatalf("boundary case: got %q", lines)
	}
}

func TestSafeFilenameNoTraversal(t *testing.T) {
	for _, in := range []string{"../../etc/passwd", "..", ".", "yt:@Handle", "a\\b"} {
		got := safeFilename(in)
		if strings.ContainsAny(got, `/\:`) || got == ".." || got == "." {
			t.Errorf("safeFilename(%q) = %q", in, got)
		}
	}
}
