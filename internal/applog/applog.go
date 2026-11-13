// Package applog sets up the log file for both apps.
//
// The logs used to be written next to the .exe and truncated on every
// start. Two problems: if the exe sits in a folder the user can't write to
// (e.g. Program Files) the file silently couldn't be opened and every log
// line went to a console a GUI app doesn't have; and restarting the app to
// look at a problem wiped the log of the run that had the problem.
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Setup opens <dir>/<name>.log for the first directory in dirs that is
// writable, after rotating an existing log to <name>.prev.log (so the
// previous run is always kept). Falls back to the OS temp dir. All log
// output also goes to stderr (visible when started from a terminal).
// Returns the path in use, or "" if no file could be opened.
func Setup(name string, dirs ...string) string {
	dirs = append(dirs, os.TempDir())
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			continue
		}
		path := filepath.Join(dir, name+".log")
		prev := filepath.Join(dir, name+".prev.log")
		if _, err := os.Stat(path); err == nil {
			_ = os.Remove(prev)
			_ = os.Rename(path, prev)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			continue
		}
		log.SetOutput(io.MultiWriter(f, os.Stderr))
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		return path
	}
	fmt.Fprintln(os.Stderr, "applog: no writable log directory, logging to stderr only")
	return ""
}
