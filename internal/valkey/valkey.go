// Package valkey provides a narrow Valkey (Redis-protocol) client used by
// Limen for short-lived, TTL-driven server state. Phase 7's one-shot OAuth
// state is the first consumer; future phases (e.g. rate-limit counters,
// sub-second caches) can reuse the same client.
//
// The Client interface deliberately exposes only the verbs we use today so
// tests can swap in an in-memory fake (see InMemory) without dragging the
// full surface of github.com/valkey-io/valkey-go into the type signature.
package valkey

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/belphemur/limen/internal/config"
	vk "github.com/valkey-io/valkey-go"
)

// ErrNotFound is returned by GetDel when the key is absent (or expired).
// Callers treat this as "first-touch wins" — the second consumer of a
// one-shot token sees ErrNotFound and rejects the request.
var ErrNotFound = errors.New("valkey: key not found")

// StreamMessage represents a single entry returned by XReadGroup.
type StreamMessage struct {
	ID     string
	Stream string
	Fields map[string]string
}

// Client is the narrow surface Limen consumes.
type Client interface {
	// SetEX writes value with a hard server-side TTL. Overwrites are
	// allowed; callers that need atomic create-or-fail should use SetNX.
	SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// SetNX sets key to value with a TTL, but only if key does not
	// already exist. Returns true if the key was set, false if the key
	// already existed.
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)

	// GetDel atomically reads and deletes the key. Returns ErrNotFound
	// when the key is absent. The atomicity is what gives Phase 7 its
	// one-shot OAuth state guarantee.
	GetDel(ctx context.Context, key string) ([]byte, error)

	// Del removes the key. No error if key does not exist.
	Del(ctx context.Context, key string) error

	// Get returns the value for key, or ErrNotFound if the key is absent
	// or expired. Unlike GetDel, this does NOT delete the key.
	Get(ctx context.Context, key string) ([]byte, error)

	// XAdd appends an entry to a stream. Returns the entry ID.
	XAdd(ctx context.Context, stream string, fields map[string]string, maxLen int64) (string, error)

	// XReadGroup reads new messages from streams for a consumer group.
	// Returns a slice of stream messages.
	XReadGroup(ctx context.Context, group, consumer string, blockMs int64, count int64, streams ...string) ([]StreamMessage, error)

	// XAck acknowledges messages in a stream for a consumer group.
	// Returns the count of acknowledged messages.
	XAck(ctx context.Context, stream, group string, ids ...string) (int64, error)

	// XDel deletes entries from a stream. Returns the count of deleted entries.
	XDel(ctx context.Context, stream string, ids ...string) (int64, error)

	// XAutoClaim transfers pending entries from another consumer to this consumer.
	// Returns claimed message IDs.
	XAutoClaim(ctx context.Context, stream, group, consumer string, minIdleMs int64, count int64) ([]string, error)

	// XGroupCreate creates a consumer group. $ means start from now (only new messages).
	XGroupCreate(ctx context.Context, stream, group, start string) error

	// Close releases the underlying connection pool.
	Close()
}

// Open dials Valkey using the supplied config. Returns a usable Client or
// a wiring error; the dial itself happens lazily on the first command (the
// valkey-go default), so this only fails on obvious config mistakes.
func Open(cfg config.ValkeyConfig) (Client, error) {
	if cfg.Address == "" {
		return nil, errors.New("valkey: address is empty")
	}
	opt := vk.ClientOption{
		InitAddress: []string{cfg.Address},
		Password:    cfg.Password,
	}
	if cfg.DialTimeout > 0 {
		opt.Dialer.Timeout = cfg.DialTimeout
	}
	c, err := vk.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("valkey: dial %s: %w", cfg.Address, err)
	}
	return &realClient{c: c}, nil
}

type realClient struct {
	c vk.Client
}

func (r *realClient) SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("valkey: ttl must be > 0")
	}
	cmd := r.c.B().Set().Key(key).Value(vk.BinaryString(value)).ExSeconds(int64(ttl.Seconds())).Build()
	if err := r.c.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("valkey: SETEX %s: %w", key, err)
	}
	return nil
}

func (r *realClient) GetDel(ctx context.Context, key string) ([]byte, error) {
	cmd := r.c.B().Getdel().Key(key).Build()
	resp := r.c.Do(ctx, cmd)
	if err := resp.Error(); err != nil {
		if vk.IsValkeyNil(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("valkey: GETDEL %s: %w", key, err)
	}
	s, err := resp.ToString()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("valkey: GETDEL decode %s: %w", key, err)
	}
	return []byte(s), nil
}

func (r *realClient) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, errors.New("valkey: ttl must be > 0")
	}
	cmd := r.c.B().Set().Key(key).Value(vk.BinaryString(value)).Nx().ExSeconds(int64(ttl.Seconds())).Build()
	resp := r.c.Do(ctx, cmd)
	s, err := resp.ToString()
	if err != nil && vk.IsValkeyNil(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("valkey: SETNX %s: %w", key, err)
	}
	return s == "OK", nil
}

func (r *realClient) Del(ctx context.Context, key string) error {
	cmd := r.c.B().Del().Key(key).Build()
	if _, err := r.c.Do(ctx, cmd).AsInt64(); err != nil {
		return fmt.Errorf("valkey: DEL %s: %w", key, err)
	}
	return nil
}

func (r *realClient) Get(ctx context.Context, key string) ([]byte, error) {
	cmd := r.c.B().Get().Key(key).Build()
	resp := r.c.Do(ctx, cmd)
	s, err := resp.ToString()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("valkey: GET %s: %w", key, err)
	}
	return []byte(s), nil
}

func (r *realClient) Close() { r.c.Close() }

func (r *realClient) XAdd(ctx context.Context, stream string, fields map[string]string, maxLen int64) (string, error) {
	if stream == "" {
		return "", errors.New("valkey: stream name is empty")
	}

	var resp vk.ValkeyResult
	if maxLen > 0 {
		b := r.c.B().Xadd().Key(stream).Maxlen().Exact().Threshold(strconv.FormatInt(maxLen, 10)).Id("*").FieldValue()
		for k, v := range fields {
			b = b.FieldValue(k, v)
		}
		resp = r.c.Do(ctx, b.Build())
	} else {
		b := r.c.B().Xadd().Key(stream).Id("*").FieldValue()
		for k, v := range fields {
			b = b.FieldValue(k, v)
		}
		resp = r.c.Do(ctx, b.Build())
	}

	s, err := resp.ToString()
	if err != nil {
		return "", fmt.Errorf("valkey: XADD %s: %w", stream, err)
	}
	return s, nil
}

func (r *realClient) XReadGroup(ctx context.Context, group, consumer string, blockMs int64, count int64, streams ...string) ([]StreamMessage, error) {
	if len(streams) == 0 {
		return nil, nil
	}
	if group == "" || consumer == "" {
		return nil, errors.New("valkey: group and consumer must not be empty")
	}

	ids := make([]string, len(streams))
	for i := range streams {
		ids[i] = ">"
	}

	var resp vk.ValkeyResult
	if count > 0 && blockMs > 0 {
		resp = r.c.Do(ctx, r.c.B().Xreadgroup().Group(group, consumer).Count(count).Block(blockMs).Streams().Key(streams...).Id(ids...).Build())
	} else if count > 0 {
		resp = r.c.Do(ctx, r.c.B().Xreadgroup().Group(group, consumer).Count(count).Streams().Key(streams...).Id(ids...).Build())
	} else if blockMs > 0 {
		resp = r.c.Do(ctx, r.c.B().Xreadgroup().Group(group, consumer).Block(blockMs).Streams().Key(streams...).Id(ids...).Build())
	} else {
		resp = r.c.Do(ctx, r.c.B().Xreadgroup().Group(group, consumer).Streams().Key(streams...).Id(ids...).Build())
	}

	msg, err := resp.ToMessage()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("valkey: XREADGROUP: %w", err)
	}
	if msg.IsNil() {
		return nil, nil
	}

	slices, err := msg.AsXReadSlices()
	if err != nil {
		return nil, fmt.Errorf("valkey: XREADGROUP: %w", err)
	}

	var messages []StreamMessage
	for _, stream := range streams {
		entries := slices[stream]
		for _, entry := range entries {
			fields := make(map[string]string, len(entry.FieldValues))
			for _, fv := range entry.FieldValues {
				fields[fv.Field] = fv.Value
			}
			messages = append(messages, StreamMessage{
				ID:     entry.ID,
				Stream: stream,
				Fields: fields,
			})
		}
	}
	return messages, nil
}

func (r *realClient) XAck(ctx context.Context, stream, group string, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	cmd := r.c.B().Xack().Key(stream).Group(group).Id(ids...).Build()
	n, err := r.c.Do(ctx, cmd).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("valkey: XACK %s: %w", stream, err)
	}
	return n, nil
}

func (r *realClient) XDel(ctx context.Context, stream string, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	cmd := r.c.B().Xdel().Key(stream).Id(ids...).Build()
	n, err := r.c.Do(ctx, cmd).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("valkey: XDEL %s: %w", stream, err)
	}
	return n, nil
}

func (r *realClient) XAutoClaim(ctx context.Context, stream, group, consumer string, minIdleMs int64, count int64) ([]string, error) {
	b := r.c.B().Xautoclaim().Key(stream).Group(group).Consumer(consumer).
		MinIdleTime(strconv.FormatInt(minIdleMs, 10)).Start("0-0")

	var resp vk.ValkeyResult
	if count > 0 {
		resp = r.c.Do(ctx, b.Count(count).Justid().Build())
	} else {
		resp = r.c.Do(ctx, b.Justid().Build())
	}

	arr, err := resp.ToArray()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("valkey: XAUTOCLAIM %s: %w", stream, err)
	}
	if len(arr) < 2 {
		return nil, fmt.Errorf("valkey: XAUTOCLAIM %s: unexpected response length %d", stream, len(arr))
	}

	idsArr, err := arr[1].ToArray()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("valkey: XAUTOCLAIM %s: %w", stream, err)
	}

	ids := make([]string, 0, len(idsArr))
	for _, idMsg := range idsArr {
		id, err := idMsg.ToString()
		if err != nil {
			return nil, fmt.Errorf("valkey: XAUTOCLAIM %s: %w", stream, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *realClient) XGroupCreate(ctx context.Context, stream, group, start string) error {
	if stream == "" || group == "" {
		return errors.New("valkey: stream and group must not be empty")
	}
	cmd := r.c.B().XgroupCreate().Key(stream).Group(group).Id(start).Mkstream().Build()
	if err := r.c.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("valkey: XGROUP CREATE %s %s: %w", stream, group, err)
	}
	return nil
}

// InMemory is a test fake honoring TTLs via wall-clock comparisons. Safe
// for concurrent use. Do not use in production.
type InMemory struct {
	mu             sync.Mutex
	entries        map[string]inMemoryEntry
	streams        map[string][]inMemoryStreamEntry
	consumerGroups map[string]map[string]consumerGroupState
	streamSeq      map[string]int64
	// Now lets tests inject a clock; defaults to time.Now.
	Now func() time.Time
}

type inMemoryEntry struct {
	value     []byte
	expiresAt time.Time
}

type inMemoryStreamEntry struct {
	ID     string
	Fields map[string]string
}

type consumerGroupState struct {
	lastDeliveredID string
	consumers       map[string][]string // consumer name -> pending message IDs
}

// NewInMemory returns an empty in-memory client.
func NewInMemory() *InMemory {
	return &InMemory{
		entries:        make(map[string]inMemoryEntry),
		streams:        make(map[string][]inMemoryStreamEntry),
		consumerGroups: make(map[string]map[string]consumerGroupState),
		streamSeq:      make(map[string]int64),
	}
}

func (m *InMemory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// SetEX implements Client.
func (m *InMemory) SetEX(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("valkey: ttl must be > 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := make([]byte, len(value))
	copy(buf, value)
	m.entries[key] = inMemoryEntry{value: buf, expiresAt: m.now().Add(ttl)}
	return nil
}

// GetDel implements Client.
func (m *InMemory) GetDel(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.entries, key)
	if !e.expiresAt.IsZero() && !m.now().Before(e.expiresAt) {
		return nil, ErrNotFound
	}
	return e.value, nil
}

func (m *InMemory) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, errors.New("valkey: ttl must be > 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if ok && (e.expiresAt.IsZero() || m.now().Before(e.expiresAt)) {
		return false, nil
	}
	buf := make([]byte, len(value))
	copy(buf, value)
	m.entries[key] = inMemoryEntry{value: buf, expiresAt: m.now().Add(ttl)}
	return true, nil
}

func (m *InMemory) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

func (m *InMemory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, ErrNotFound
	}
	if !e.expiresAt.IsZero() && !m.now().Before(e.expiresAt) {
		return nil, ErrNotFound
	}
	buf := make([]byte, len(e.value))
	copy(buf, e.value)
	return buf, nil
}

// Close is a no-op for the in-memory fake.
func (m *InMemory) Close() {}

func (m *InMemory) XAdd(_ context.Context, stream string, fields map[string]string, maxLen int64) (string, error) {
	if stream == "" {
		return "", errors.New("valkey: stream name is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	seq := m.streamSeq[stream]
	id := fmt.Sprintf("%d-%d", m.now().UnixMilli(), seq)
	m.streamSeq[stream] = seq + 1

	entryFields := make(map[string]string, len(fields))
	maps.Copy(entryFields, fields)

	m.streams[stream] = append(m.streams[stream], inMemoryStreamEntry{
		ID:     id,
		Fields: entryFields,
	})

	if maxLen > 0 && int64(len(m.streams[stream])) > maxLen {
		trim := int64(len(m.streams[stream])) - maxLen
		m.streams[stream] = m.streams[stream][trim:]
	}

	return id, nil
}

func (m *InMemory) XReadGroup(_ context.Context, group, consumer string, blockMs int64, count int64, streams ...string) ([]StreamMessage, error) {
	if len(streams) == 0 {
		return nil, nil
	}
	if group == "" || consumer == "" {
		return nil, errors.New("valkey: group and consumer must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var messages []StreamMessage
	for _, stream := range streams {
		groups, ok := m.consumerGroups[stream]
		if !ok {
			return nil, fmt.Errorf("valkey: no consumer groups for stream %s", stream)
		}
		state, ok := groups[group]
		if !ok {
			return nil, fmt.Errorf("valkey: consumer group %s does not exist for stream %s", group, stream)
		}

		entries := m.streams[stream]
		var delivered int64
		for _, entry := range entries {
			if compareStreamID(entry.ID, state.lastDeliveredID) <= 0 {
				continue
			}
			if count > 0 && delivered >= count {
				break
			}

			fields := make(map[string]string, len(entry.Fields))
			maps.Copy(fields, entry.Fields)
			messages = append(messages, StreamMessage{
				ID:     entry.ID,
				Stream: stream,
				Fields: fields,
			})

			state.lastDeliveredID = entry.ID
			state.consumers[consumer] = append(state.consumers[consumer], entry.ID)
			delivered++
		}

		groups[group] = state
	}

	return messages, nil
}

func (m *InMemory) XAck(_ context.Context, stream, group string, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	groups, ok := m.consumerGroups[stream]
	if !ok {
		return 0, nil
	}
	state, ok := groups[group]
	if !ok {
		return 0, nil
	}

	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}

	var count int64
	for consumer, pending := range state.consumers {
		filtered := make([]string, 0, len(pending))
		for _, id := range pending {
			if _, ok := idSet[id]; ok {
				count++
			} else {
				filtered = append(filtered, id)
			}
		}
		state.consumers[consumer] = filtered
	}
	groups[group] = state
	return count, nil
}

func (m *InMemory) XDel(_ context.Context, stream string, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, ok := m.streams[stream]
	if !ok {
		return 0, nil
	}

	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}

	var count int64
	filtered := make([]inMemoryStreamEntry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := idSet[entry.ID]; ok {
			count++
		} else {
			filtered = append(filtered, entry)
		}
	}
	m.streams[stream] = filtered
	return count, nil
}

func (m *InMemory) XAutoClaim(_ context.Context, stream, group, consumer string, minIdleMs int64, count int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	groups, ok := m.consumerGroups[stream]
	if !ok {
		return nil, fmt.Errorf("valkey: no consumer groups for stream %s", stream)
	}
	state, ok := groups[group]
	if !ok {
		return nil, fmt.Errorf("valkey: consumer group %s does not exist for stream %s", group, stream)
	}

	var claimed []string
	for otherConsumer, pending := range state.consumers {
		if otherConsumer == consumer {
			continue
		}
		var remaining []string
		for _, id := range pending {
			if count > 0 && int64(len(claimed)) >= count {
				remaining = append(remaining, id)
				continue
			}
			claimed = append(claimed, id)
		}
		state.consumers[otherConsumer] = remaining
	}

	if len(claimed) > 0 {
		state.consumers[consumer] = append(state.consumers[consumer], claimed...)
	}
	groups[group] = state
	return claimed, nil
}

func (m *InMemory) XGroupCreate(_ context.Context, stream, group, start string) error {
	if stream == "" || group == "" {
		return errors.New("valkey: stream and group must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.consumerGroups[stream] == nil {
		m.consumerGroups[stream] = make(map[string]consumerGroupState)
	}

	if _, exists := m.consumerGroups[stream][group]; exists {
		return fmt.Errorf("valkey: consumer group %s already exists for stream %s", group, stream)
	}

	var lastDeliveredID string
	if start == "$" {
		entries := m.streams[stream]
		if len(entries) > 0 {
			lastDeliveredID = entries[len(entries)-1].ID
		} else {
			lastDeliveredID = "0-0"
		}
	} else {
		lastDeliveredID = start
	}

	m.consumerGroups[stream][group] = consumerGroupState{
		lastDeliveredID: lastDeliveredID,
		consumers:       make(map[string][]string),
	}
	return nil
}

func compareStreamID(a, b string) int {
	aParts := strings.Split(a, "-")
	bParts := strings.Split(b, "-")

	aMs, _ := strconv.ParseInt(aParts[0], 10, 64)
	bMs, _ := strconv.ParseInt(bParts[0], 10, 64)
	if aMs != bMs {
		if aMs < bMs {
			return -1
		}
		return 1
	}

	aSeq, _ := strconv.ParseInt(aParts[1], 10, 64)
	bSeq, _ := strconv.ParseInt(bParts[1], 10, 64)
	if aSeq != bSeq {
		if aSeq < bSeq {
			return -1
		}
		return 1
	}
	return 0
}
