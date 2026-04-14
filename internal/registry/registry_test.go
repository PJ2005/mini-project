package registry

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLatestMessageCachePersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "registry.db")

	r, err := New(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}

	for i := 0; i < 10000; i++ {
		deviceID := fmt.Sprintf("device-%05d", i)
		data := []byte(fmt.Sprintf("payload-%05d", i))
		if err := r.UpsertLatestMessage(deviceID, data); err != nil {
			t.Fatalf("upsert latest message %d: %v", i, err)
		}
		got, err := r.GetLatestMessage(deviceID)
		if err != nil {
			t.Fatalf("get latest message %d: %v", i, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("latest message %d = %q, want %q", i, got, data)
		}
	}

	if err := r.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	reopened, err := New(dbPath)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	defer reopened.Close()

	for i := 0; i < 10000; i++ {
		deviceID := fmt.Sprintf("device-%05d", i)
		want := []byte(fmt.Sprintf("payload-%05d", i))
		got, err := reopened.GetLatestMessage(deviceID)
		if err != nil {
			t.Fatalf("reopened get latest message %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("reopened latest message %d = %q, want %q", i, got, want)
		}
	}
}
