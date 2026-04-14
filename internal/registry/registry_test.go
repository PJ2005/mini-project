package registry

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"
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

func TestRegistryCRUDAndCounters(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer r.Close()

	if err := r.Register(Device{DeviceID: "dev-a", Name: "dev-a", Protocol: "mqtt", Status: "active", Metadata: map[string]string{"zone": "lab"}}); err != nil {
		t.Fatalf("register dev-a: %v", err)
	}
	if err := r.Register(Device{DeviceID: "dev-b", Name: "dev-b", Protocol: "http", Status: "active"}); err != nil {
		t.Fatalf("register dev-b: %v", err)
	}

	d, err := r.GetByID("dev-a")
	if err != nil || d == nil {
		t.Fatalf("get by id dev-a: %v, %v", d, err)
	}
	if d.Metadata["zone"] != "lab" {
		t.Fatalf("metadata zone = %q, want %q", d.Metadata["zone"], "lab")
	}

	httpDevices, err := r.GetByProtocol("http")
	if err != nil {
		t.Fatalf("get by protocol: %v", err)
	}
	if len(httpDevices) != 1 || httpDevices[0].DeviceID != "dev-b" {
		t.Fatalf("unexpected protocol devices: %#v", httpDevices)
	}

	if err := r.UpdateStatus("dev-b", "inactive"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if err := r.UpdateLastSeen("dev-a"); err != nil {
		t.Fatalf("update last seen: %v", err)
	}

	if _, err := r.MarkInactiveDevices(0); err != nil {
		t.Fatalf("mark inactive: %v", err)
	}

	if count, err := r.DeviceCount(); err != nil || count != 2 {
		t.Fatalf("device count = %d, err=%v", count, err)
	}
	if byProto, err := r.DeviceCountByProtocol(); err != nil || byProto["mqtt"] != 1 || byProto["http"] != 1 {
		t.Fatalf("device count by protocol = %#v, err=%v", byProto, err)
	}
	if byStatus, err := r.DeviceCountByStatus(); err != nil {
		t.Fatalf("device count by status err: %v", err)
	} else if len(byStatus) == 0 {
		t.Fatal("device count by status is empty")
	}

	if err := r.InsertDeadLetter("iot.test.dev-a", []byte("bad"), "publish failed"); err != nil {
		t.Fatalf("insert dead letter: %v", err)
	}
	if n, err := r.DeadLetterCount(); err != nil || n != 1 {
		t.Fatalf("dead letter count = %d, err=%v", n, err)
	}

	if err := r.UpsertLatestMessage("dev-a", []byte("payload-a")); err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := r.LatestMessageCount()
		if err != nil {
			t.Fatalf("latest message count err=%v", err)
		}
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest message count still %d after flush wait", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	list, err := r.ListDevices()
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("list devices len = %d, want >=2", len(list))
	}
}
