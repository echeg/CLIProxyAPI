package management

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteConfigBytesReturnsWrittenBytes checks that writeConfigBytes reports
// the normalized bytes that land on disk, not the input it was given.
func TestWriteConfigBytesReturnsWrittenBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Flow style and an indented standalone comment are both rewritten.
	input := []byte("config-version: 8\nserver: {port: 8317}\naccess:\n    # indented comment\n    api-keys: [k1]\n")
	written, err := writeConfigBytes(path, input)
	if err != nil {
		t.Fatalf("writeConfigBytes: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, onDisk) {
		t.Fatalf("returned bytes differ from the file:\nreturned:\n%s\nfile:\n%s", written, onDisk)
	}
	if bytes.Equal(written, input) {
		t.Fatalf("expected normalized bytes, got the input back:\n%s", written)
	}
	if !bytes.Contains(written, []byte("\n# indented comment\n")) {
		t.Fatalf("standalone comment not left aligned:\n%s", written)
	}
}

func TestWriteConfigBytesInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	written, err := writeConfigBytes(path, []byte("server: [unterminated\n"))
	if err == nil {
		t.Fatal("expected an error for invalid YAML")
	}
	if written != nil {
		t.Fatalf("expected no bytes on error, got %q", written)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("invalid YAML must not create the file: stat err=%v", errStat)
	}
}

func TestWriteConfigBytesWriteFailure(t *testing.T) {
	// A directory cannot be opened for writing.
	written, err := writeConfigBytes(t.TempDir(), []byte("config-version: 8\n"))
	if err == nil {
		t.Fatal("expected an error when the path is a directory")
	}
	if written != nil {
		t.Fatalf("expected no bytes on error, got %q", written)
	}
}
