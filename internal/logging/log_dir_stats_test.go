package logging

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeStatsFixture(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), errMkdir)
	}
	if errWrite := os.WriteFile(path, make([]byte, size), 0o644); errWrite != nil {
		t.Fatalf("write %s: %v", path, errWrite)
	}
	if errTimes := os.Chtimes(path, modTime, modTime); errTimes != nil {
		t.Fatalf("chtimes %s: %v", path, errTimes)
	}
}

func setDirTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if errTimes := os.Chtimes(path, modTime, modTime); errTimes != nil {
		t.Fatalf("chtimes %s: %v", path, errTimes)
	}
}

func scanOrFail(t *testing.T, dir string) LogDirStats {
	t.Helper()
	stats, errScan := ScanLogDir(dir)
	if errScan != nil {
		t.Fatalf("ScanLogDir(%q) error: %v", dir, errScan)
	}
	return stats
}

func assertZeroStats(t *testing.T, stats LogDirStats) {
	t.Helper()
	if stats != (LogDirStats{}) {
		t.Fatalf("expected zero stats, got %+v", stats)
	}
}

// buildStatsFixture creates the shared fixture and returns the expected oldest
// and newest file mtimes.
func buildStatsFixture(t *testing.T, dir string) (time.Time, time.Time) {
	t.Helper()
	zone := time.FixedZone("UTC+3", 3*60*60)
	oldest := time.Date(2026, 11, 1, 10, 0, 0, 0, zone)
	newest := time.Date(2026, 11, 5, 15, 30, 45, 0, zone)
	mid := time.Date(2026, 11, 3, 12, 0, 0, 0, zone)

	writeStatsFixture(t, filepath.Join(dir, "a.log"), 100, mid)
	writeStatsFixture(t, filepath.Join(dir, "B.LOG"), 200, mid)
	writeStatsFixture(t, filepath.Join(dir, "c.log.gz"), 300, mid)
	writeStatsFixture(t, filepath.Join(dir, "request-body-1.tmp"), 7, oldest)
	writeStatsFixture(t, filepath.Join(dir, "sub", "nested.log"), 40, mid)
	writeStatsFixture(t, filepath.Join(dir, "request-log-parts-x", "part.bin"), 5, newest)
	if errMkdir := os.Mkdir(filepath.Join(dir, "dir.log"), 0o755); errMkdir != nil {
		t.Fatalf("mkdir dir.log: %v", errMkdir)
	}

	// Directory mtimes far outside the file range must never be reported.
	setDirTime(t, filepath.Join(dir, "sub"), time.Date(2001, 1, 1, 0, 0, 0, 0, zone))
	setDirTime(t, filepath.Join(dir, "request-log-parts-x"), time.Date(2040, 1, 1, 0, 0, 0, 0, zone))
	setDirTime(t, filepath.Join(dir, "dir.log"), time.Date(2000, 1, 1, 0, 0, 0, 0, zone))
	return oldest, newest
}

func assertFixtureStats(t *testing.T, stats LogDirStats, oldest, newest time.Time) {
	t.Helper()
	// a.log + B.LOG + c.log.gz + request-body-1.tmp + sub/nested.log + part.bin
	if stats.TotalBytes != 652 || stats.TotalFiles != 6 {
		t.Fatalf("total = %d bytes / %d files, want 652 / 6", stats.TotalBytes, stats.TotalFiles)
	}
	// a.log + B.LOG + c.log.gz only; sub/nested.log is not top level.
	if stats.CappedBytes != 600 || stats.CappedFiles != 3 {
		t.Fatalf("capped = %d bytes / %d files, want 600 / 3", stats.CappedBytes, stats.CappedFiles)
	}
	if !stats.Oldest.Equal(oldest) {
		t.Fatalf("oldest = %v, want %v", stats.Oldest, oldest)
	}
	if !stats.Newest.Equal(newest) {
		t.Fatalf("newest = %v, want %v", stats.Newest, newest)
	}
}

func TestScanLogDir_CountsTotalsAndCappedFiles(t *testing.T) {
	dir := t.TempDir()
	oldest, newest := buildStatsFixture(t, dir)
	assertFixtureStats(t, scanOrFail(t, dir), oldest, newest)
}

func TestScanLogDir_TrailingSlashMatchesCleanPath(t *testing.T) {
	dir := t.TempDir()
	oldest, newest := buildStatsFixture(t, dir)
	clean := scanOrFail(t, dir)
	slashed := scanOrFail(t, dir+string(os.PathSeparator))
	if clean != slashed {
		t.Fatalf("trailing slash stats %+v differ from clean %+v", slashed, clean)
	}
	assertFixtureStats(t, slashed, oldest, newest)
}

func TestScanLogDir_MissingDirectoryReturnsZero(t *testing.T) {
	assertZeroStats(t, scanOrFail(t, filepath.Join(t.TempDir(), "missing")))
}

func TestScanLogDir_EmptyDirectoryReturnsZero(t *testing.T) {
	stats := scanOrFail(t, t.TempDir())
	assertZeroStats(t, stats)
	if !stats.Oldest.IsZero() || !stats.Newest.IsZero() {
		t.Fatalf("expected zero times, got oldest=%v newest=%v", stats.Oldest, stats.Newest)
	}
}

func TestScanLogDir_BlankPathReturnsZero(t *testing.T) {
	assertZeroStats(t, scanOrFail(t, ""))
	assertZeroStats(t, scanOrFail(t, "   "))
}

func TestScanLogDir_RootRegularFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.log")
	writeStatsFixture(t, path, 10, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if _, errScan := ScanLogDir(path); errScan == nil {
		t.Fatal("expected an error for a root that is a regular file")
	}
}

func TestScanLogDir_PathUnderRegularFileReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows reports ERROR_PATH_NOT_FOUND, which os.IsNotExist accepts, so
		// the root reads as missing there, matching the cleaner's os.ReadDir.
		t.Skip("Windows has no ENOTDIR; a path below a file reads as missing")
	}
	file := filepath.Join(t.TempDir(), "file")
	writeStatsFixture(t, file, 1, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	// ENOTDIR is not "not exist", so it must not be reported as an empty directory.
	if _, errScan := ScanLogDir(filepath.Join(file, "logs")); errScan == nil {
		t.Fatal("expected an error for a root below a regular file")
	}
}

func TestScanLogDir_UnreadableRootReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if errChmod := os.Chmod(dir, 0o000); errChmod != nil {
		t.Fatalf("chmod root: %v", errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(dir, 0o755); errChmod != nil {
			t.Errorf("restore root permissions: %v", errChmod)
		}
	})
	if _, errScan := ScanLogDir(dir); errScan == nil {
		t.Fatal("expected an error for an unreadable root")
	}
}

// The capped stats must be exactly the total the cleaner enforces its limit on.
func TestScanLogDir_CappedBytesMatchCleanerTotal(t *testing.T) {
	dir := t.TempDir()
	buildStatsFixture(t, dir)
	stats := scanOrFail(t, dir)

	deleted, errClean := enforceLogDirSizeLimit(dir, stats.CappedBytes, "")
	if errClean != nil || deleted != 0 {
		t.Fatalf("limit = capped bytes: deleted %d, err %v; want 0, nil", deleted, errClean)
	}
	deleted, errClean = enforceLogDirSizeLimit(dir, stats.CappedBytes-1, "")
	if errClean != nil || deleted == 0 {
		t.Fatalf("limit = capped bytes - 1: deleted %d, err %v; want > 0, nil", deleted, errClean)
	}
}

func TestScanLogDir_SymlinkedRootMatchesTarget(t *testing.T) {
	target := t.TempDir()
	oldest, newest := buildStatsFixture(t, target)
	link := filepath.Join(t.TempDir(), "logs")
	if errLink := os.Symlink(target, link); errLink != nil {
		t.Skipf("symlinks unavailable: %v", errLink)
	}
	stats := scanOrFail(t, link)
	if stats != scanOrFail(t, target) {
		t.Fatalf("symlinked root stats %+v differ from target", stats)
	}
	assertFixtureStats(t, stats, oldest, newest)
}

func TestScanLogDir_SkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	modTime := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	realFile := filepath.Join(dir, "real.log")
	writeStatsFixture(t, realFile, 10, modTime)
	outside := filepath.Join(t.TempDir(), "outside.log")
	writeStatsFixture(t, outside, 1000, modTime)
	outsideDir := t.TempDir()
	writeStatsFixture(t, filepath.Join(outsideDir, "big.log"), 5000, modTime)
	if errMkdir := os.Mkdir(filepath.Join(dir, "sub"), 0o755); errMkdir != nil {
		t.Fatalf("mkdir sub: %v", errMkdir)
	}

	links := map[string]string{
		filepath.Join(dir, "link.log"):            realFile,
		filepath.Join(dir, "dangling.log"):        filepath.Join(dir, "missing.log"),
		filepath.Join(dir, "linkdir"):             outsideDir,
		filepath.Join(dir, "sub", "link.log"):     outside,
		filepath.Join(dir, "sub", "dangling.log"): filepath.Join(dir, "sub", "missing.log"),
	}
	for link, target := range links {
		if errLink := os.Symlink(target, link); errLink != nil {
			t.Skipf("symlinks unavailable: %v", errLink)
		}
	}

	stats := scanOrFail(t, dir)
	if stats.TotalBytes != 10 || stats.TotalFiles != 1 {
		t.Fatalf("total = %d bytes / %d files, want 10 / 1", stats.TotalBytes, stats.TotalFiles)
	}
	if stats.CappedBytes != 10 || stats.CappedFiles != 1 {
		t.Fatalf("capped = %d bytes / %d files, want 10 / 1", stats.CappedBytes, stats.CappedFiles)
	}
}

func TestScanLogDir_SkipsUnreadableSubdirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	modTime := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	writeStatsFixture(t, filepath.Join(dir, "main.log"), 10, modTime)
	writeStatsFixture(t, filepath.Join(dir, "ok", "part.bin"), 20, modTime)
	locked := filepath.Join(dir, "locked")
	writeStatsFixture(t, filepath.Join(locked, "hidden.log"), 1000, modTime)
	if errChmod := os.Chmod(locked, 0o000); errChmod != nil {
		t.Fatalf("chmod locked: %v", errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(locked, 0o755); errChmod != nil {
			t.Errorf("restore locked permissions: %v", errChmod)
		}
	})

	stats := scanOrFail(t, dir)
	if stats.TotalBytes != 30 || stats.TotalFiles != 2 {
		t.Fatalf("total = %d bytes / %d files, want 30 / 2", stats.TotalBytes, stats.TotalFiles)
	}
	if stats.CappedBytes != 10 || stats.CappedFiles != 1 {
		t.Fatalf("capped = %d bytes / %d files, want 10 / 1", stats.CappedBytes, stats.CappedFiles)
	}
}
