package host

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.conf")
	h := Native{}
	for _, value := range []string{"first", "replacement"} {
		if err := h.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := h.ReadFile(path)
		if err != nil || string(got) != value {
			t.Fatalf("content=%s %v", got, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("incorrect permissions")
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary files leaked")
	}
	if err := h.WriteFile(filepath.Join(path, "missing"), nil, 0600); err == nil {
		t.Fatal("invalid directory accepted")
	}
	if err := h.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestCommandOutputBound(t *testing.T) {
	var output limitedBuffer
	data := bytes.Repeat([]byte{'x'}, (1<<20)+50)
	if n, err := output.Write(data); n != len(data) || err != nil {
		t.Fatal("writer did not drain output")
	}
	if !output.overflow || output.Len() != 1<<20 {
		t.Fatal("unbounded output")
	}
	if _, err := output.Write(data); err != nil || output.Len() != 1<<20 {
		t.Fatal("overflow handling failed")
	}
}
