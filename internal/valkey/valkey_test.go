package valkey

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestInMemory_SetExGetDelOneShot(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	got, err := m.GetDel(ctx, "k")
	if err != nil {
		t.Fatalf("GetDel: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("value = %q, want %q", got, "v")
	}
	// One-shot: second read is gone.
	if _, err := m.GetDel(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Expiry(t *testing.T) {
	m := NewInMemory()
	now := time.Unix(1_700_000_000, 0)
	m.Now = func() time.Time { return now }
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), 5*time.Second); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	// Advance past TTL.
	now = now.Add(10 * time.Second)
	if _, err := m.GetDel(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Missing(t *testing.T) {
	m := NewInMemory()
	if _, err := m.GetDel(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_ZeroTTLRejected(t *testing.T) {
	m := NewInMemory()
	if err := m.SetEX(context.Background(), "k", []byte("v"), 0); err == nil {
		t.Fatalf("SetEX with zero TTL should error")
	}
}

func TestInMemory_SetNX(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	ok, err := m.SetNX(ctx, "k", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("first SetNX: %v", err)
	}
	if !ok {
		t.Fatalf("first SetNX returned false, want true")
	}

	ok, err = m.SetNX(ctx, "k", []byte("v2"), time.Minute)
	if err != nil {
		t.Fatalf("second SetNX: %v", err)
	}
	if ok {
		t.Fatalf("second SetNX returned true, want false")
	}
}

func TestInMemory_Del(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	if err := m.Del(ctx, "k"); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if _, err := m.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Del err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Get(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	v, err := m.Get(ctx, "k")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if string(v) != "v" {
		t.Fatalf("value = %q, want %q", v, "v")
	}

	v, err = m.Get(ctx, "k")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if string(v) != "v" {
		t.Fatalf("value after second Get = %q, want %q (should not be deleted)", v, "v")
	}
}

func TestInMemory_Get_Expired(t *testing.T) {
	m := NewInMemory()
	now := time.Unix(1_700_000_000, 0)
	m.Now = func() time.Time { return now }
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), 5*time.Second); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	now = now.Add(10 * time.Second)
	if _, err := m.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_XAdd(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	id, err := m.XAdd(ctx, "s", map[string]string{"k": "v"}, 0)
	if err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if id == "" {
		t.Fatalf("XAdd returned empty id")
	}

	if len(m.streams["s"]) != 1 {
		t.Fatalf("stream len = %d, want 1", len(m.streams["s"]))
	}
	if m.streams["s"][0].Fields["k"] != "v" {
		t.Fatalf("field = %q, want v", m.streams["s"][0].Fields["k"])
	}
}

func TestInMemory_XAdd_MaxLen(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	var ids []string
	for i := range 5 {
		id, err := m.XAdd(ctx, "s", map[string]string{"i": strconv.Itoa(i)}, 3)
		if err != nil {
			t.Fatalf("XAdd: %v", err)
		}
		ids = append(ids, id)
	}

	if len(m.streams["s"]) != 3 {
		t.Fatalf("stream len = %d, want 3", len(m.streams["s"]))
	}
	if m.streams["s"][0].ID != ids[2] {
		t.Fatalf("first remaining id = %s, want %s", m.streams["s"][0].ID, ids[2])
	}
}

func TestInMemory_XGroupCreate_XReadGroup(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	id1, err := m.XAdd(ctx, "s", map[string]string{"k": "v1"}, 0)
	if err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	id2, err := m.XAdd(ctx, "s", map[string]string{"k": "v2"}, 0)
	if err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	if err := m.XGroupCreate(ctx, "s", "g", "0"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}

	msgs, err := m.XReadGroup(ctx, "g", "c", 0, 10, "s")
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if msgs[0].ID != id1 {
		t.Fatalf("first msg id = %s, want %s", msgs[0].ID, id1)
	}
	if msgs[1].ID != id2 {
		t.Fatalf("second msg id = %s, want %s", msgs[1].ID, id2)
	}

	// Second read should return nothing (all messages already delivered to group)
	msgs, err = m.XReadGroup(ctx, "g", "c", 0, 10, "s")
	if err != nil {
		t.Fatalf("second XReadGroup: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("second read len = %d, want 0", len(msgs))
	}
}

func TestInMemory_XGroupCreate_Dollar(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	_, _ = m.XAdd(ctx, "s", map[string]string{"k": "v1"}, 0)
	if err := m.XGroupCreate(ctx, "s", "g", "$"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}
	id3, _ := m.XAdd(ctx, "s", map[string]string{"k": "v3"}, 0)

	msgs, err := m.XReadGroup(ctx, "g", "c", 0, 10, "s")
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].ID != id3 {
		t.Fatalf("msg id = %s, want %s", msgs[0].ID, id3)
	}
}

func TestInMemory_XReadGroup_Count(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	for i := range 5 {
		_, _ = m.XAdd(ctx, "s", map[string]string{"i": strconv.Itoa(i)}, 0)
	}
	if err := m.XGroupCreate(ctx, "s", "g", "0"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}

	msgs, err := m.XReadGroup(ctx, "g", "c", 0, 2, "s")
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}

	msgs, err = m.XReadGroup(ctx, "g", "c", 0, 10, "s")
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("second read len = %d, want 3", len(msgs))
	}
}

func TestInMemory_XAck(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	id1, _ := m.XAdd(ctx, "s", map[string]string{"k": "v1"}, 0)
	if err := m.XGroupCreate(ctx, "s", "g", "0"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}
	_, _ = m.XReadGroup(ctx, "g", "c", 0, 10, "s")

	n, err := m.XAck(ctx, "s", "g", id1)
	if err != nil {
		t.Fatalf("XAck: %v", err)
	}
	if n != 1 {
		t.Fatalf("XAck count = %d, want 1", n)
	}

	state := m.consumerGroups["s"]["g"]
	if len(state.consumers["c"]) != 0 {
		t.Fatalf("pending count = %d, want 0", len(state.consumers["c"]))
	}
}

func TestInMemory_XDel(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	id1, _ := m.XAdd(ctx, "s", map[string]string{"k": "v1"}, 0)
	id2, _ := m.XAdd(ctx, "s", map[string]string{"k": "v2"}, 0)

	n, err := m.XDel(ctx, "s", id1)
	if err != nil {
		t.Fatalf("XDel: %v", err)
	}
	if n != 1 {
		t.Fatalf("XDel count = %d, want 1", n)
	}

	if len(m.streams["s"]) != 1 {
		t.Fatalf("stream len = %d, want 1", len(m.streams["s"]))
	}
	if m.streams["s"][0].ID != id2 {
		t.Fatalf("remaining id = %s, want %s", m.streams["s"][0].ID, id2)
	}
}

func TestInMemory_XAutoClaim(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	id1, _ := m.XAdd(ctx, "s", map[string]string{"k": "v1"}, 0)
	if err := m.XGroupCreate(ctx, "s", "g", "0"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}
	_, _ = m.XReadGroup(ctx, "g", "c1", 0, 10, "s")

	claimed, err := m.XAutoClaim(ctx, "s", "g", "c2", 0, 10)
	if err != nil {
		t.Fatalf("XAutoClaim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed len = %d, want 1", len(claimed))
	}
	if claimed[0] != id1 {
		t.Fatalf("claimed id = %s, want %s", claimed[0], id1)
	}

	state := m.consumerGroups["s"]["g"]
	if len(state.consumers["c1"]) != 0 {
		t.Fatalf("c1 pending = %d, want 0", len(state.consumers["c1"]))
	}
	if len(state.consumers["c2"]) != 1 {
		t.Fatalf("c2 pending = %d, want 1", len(state.consumers["c2"]))
	}
}

func TestInMemory_XAutoClaim_Count(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	for i := range 3 {
		_, _ = m.XAdd(ctx, "s", map[string]string{"i": strconv.Itoa(i)}, 0)
	}
	if err := m.XGroupCreate(ctx, "s", "g", "0"); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}
	_, _ = m.XReadGroup(ctx, "g", "c1", 0, 10, "s")

	claimed, err := m.XAutoClaim(ctx, "s", "g", "c2", 0, 2)
	if err != nil {
		t.Fatalf("XAutoClaim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed len = %d, want 2", len(claimed))
	}

	state := m.consumerGroups["s"]["g"]
	if len(state.consumers["c1"]) != 1 {
		t.Fatalf("c1 pending = %d, want 1", len(state.consumers["c1"]))
	}
	if len(state.consumers["c2"]) != 2 {
		t.Fatalf("c2 pending = %d, want 2", len(state.consumers["c2"]))
	}
}
