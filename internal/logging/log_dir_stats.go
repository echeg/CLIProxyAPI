package logging

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogDirStats summarizes the disk usage of a logs directory.
//
// Total* covers every regular file in the directory tree. Capped* covers only
// the top-level files the logs-max-total-size-mb cleaner counts. Bytes are
// logical file lengths, matching the cleaner's sum. Oldest and Newest are the
// extreme modification times over counted files and are zero when none were
// counted.
type LogDirStats struct {
	TotalBytes  int64
	TotalFiles  int64
	CappedBytes int64
	CappedFiles int64
	Oldest      time.Time
	Newest      time.Time
}

// ScanLogDir walks dir and returns its LogDirStats.
//
// A blank or missing directory yields zero stats and a nil error. Only errors on
// the root itself are returned; entries below the root that cannot be read are
// skipped, as the cleaner does. Symlinks and other non-regular entries below the
// root are never counted. The scan is not atomic while logging continues.
func ScanLogDir(dir string) (LogDirStats, error) {
	var stats LogDirStats

	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return stats, nil
	}
	root := filepath.Clean(trimmed)

	rootInfo, errStat := os.Stat(root)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			return stats, nil
		}
		return stats, fmt.Errorf("logging: stat log directory: %w", errStat)
	}
	if !rootInfo.IsDir() {
		return stats, fmt.Errorf("logging: log directory %s is not a directory", root)
	}

	entries, errRead := os.ReadDir(root)
	if errRead != nil {
		return stats, fmt.Errorf("logging: read log directory: %w", errRead)
	}

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			stats.walkSubdir(path)
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil || !info.Mode().IsRegular() {
			continue
		}
		stats.addTotal(info)
		if isLogFileName(entry.Name()) {
			stats.CappedBytes += info.Size()
			stats.CappedFiles++
		}
	}

	return stats, nil
}

// walkSubdir counts the regular files below a top-level subdirectory into the
// totals, skipping anything that cannot be read.
func (s *LogDirStats) walkSubdir(dir string) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, errWalk error) error {
		if errWalk != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d == nil || !d.Type().IsRegular() {
			return nil
		}
		info, errInfo := d.Info()
		if errInfo != nil || !info.Mode().IsRegular() {
			return nil
		}
		s.addTotal(info)
		return nil
	})
}

func (s *LogDirStats) addTotal(info fs.FileInfo) {
	s.TotalBytes += info.Size()
	s.TotalFiles++
	modTime := info.ModTime()
	if s.Oldest.IsZero() || modTime.Before(s.Oldest) {
		s.Oldest = modTime
	}
	if s.Newest.IsZero() || modTime.After(s.Newest) {
		s.Newest = modTime
	}
}
