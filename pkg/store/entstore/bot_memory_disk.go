package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// SQL holds one bounded reference, never memory text, evidence, embeddings or a
// source-record index. Immutable catalogue snapshots make rollback safe: an
// interrupted write leaves only an unreferenced object, not partially edited claims.
const botMemoryHeadKey = "bot_memory_head_v1"

type botDiskMemoryEntry struct {
	Row      store.MemorySearchResult `json:"row"`
	Metadata map[string]any           `json:"metadata,omitempty"`
}
type botDiskMemoryCatalogue struct {
	Format  int                  `json:"format"`
	BotID   store.BotID          `json:"bot_id"`
	Entries []botDiskMemoryEntry `json:"entries"`
}

func memoryBackendIdentity(db botDB) *botContentCodec {
	switch d := db.(type) {
	case personalBotDB:
		return d.content
	case teamBotDB:
		return d.content
	}
	return nil
}

func memoryCodec(db botDB) *botContentCodec {
	switch d := db.(type) {
	case personalBotDB:
		return d.contentCodec()
	case teamBotDB:
		return d.contentCodec()
	}
	return nil
}
func readBotDiskMemory(ctx context.Context, db botDB, id store.BotID) (*botDiskMemoryCatalogue, error) {
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	value, ok := rec.Metadata[botMemoryHeadKey]
	if !ok {
		return &botDiskMemoryCatalogue{Format: 1, BotID: id}, nil
	}
	var ref store.BotContentRef
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &ref); err != nil {
		return nil, err
	}
	if ref.Validate() != nil || ref.Scope.BotID != id || ref.Kind != store.BotContentMemory {
		return nil, store.ErrBotInvalid
	}
	c := memoryCodec(db)
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	disk, err := c.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer disk.Close()
	stream, err := disk.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	var cat botDiskMemoryCatalogue
	decoder := json.NewDecoder(stream)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&cat)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	if err = errors.Join(err, stream.Close()); err != nil {
		return nil, err
	}
	if cat.Format != 1 || cat.BotID != id {
		return nil, store.ErrBotConflict
	}
	seen := map[string]bool{}
	for _, e := range cat.Entries {
		if _, err := uuid.Parse(e.Row.ID); err != nil || seen[e.Row.ID] {
			return nil, store.ErrBotInvalid
		}
		seen[e.Row.ID] = true
		if e.Row.Attribution != nil && e.Row.Attribution.BotID != id {
			return nil, store.ErrBotForbidden
		}
	}
	return &cat, nil
}
func writeBotDiskMemory(ctx context.Context, db botDB, cat *botDiskMemoryCatalogue) error {
	ref, err := stageBotDiskMemory(ctx, db, cat)
	if err != nil {
		return err
	}
	return commitBotDiskMemory(ctx, db, cat.BotID, ref)
}

// Stage immutable bytes before entering the mutation transaction. No SQL head
// becomes visible until fresh authority and the previous catalogue pass CAS.
func stageBotDiskMemory(ctx context.Context, db botDB, cat *botDiskMemoryCatalogue) (*store.BotContentRef, error) {
	rec, err := db.GetBot(ctx, cat.BotID)
	if err != nil {
		return nil, err
	}
	c := memoryCodec(db)
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, cat.BotID)
	if err != nil {
		return nil, err
	}
	ref, err := disk.Put(ctx, store.BotContentMemory, rec.Bot.Revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err = errors.Join(err, disk.Close()); err != nil {
		return nil, err
	}
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != cat.BotID || ref.Kind != store.BotContentMemory {
		return nil, store.ErrBotConflict
	}
	return ref, nil
}

func commitBotDiskMemory(ctx context.Context, db botDB, id store.BotID, ref *store.BotContentRef) error {
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return err
	}
	data := make(map[string]any, len(rec.Metadata)+1)
	for k, v := range rec.Metadata {
		data[k] = v
	}
	data[botMemoryHeadKey] = ref
	var n int
	switch d := db.(type) {
	case personalBotDB:
		n, err = d.client.Bot.Update().Where(personalbot.IDEQ(id), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.LastSeqEQ(rec.LastSequence)).SetData(data).Save(ctx)
	case teamBotDB:
		n, err = d.client.Bot.Update().Where(teambot.IDEQ(id), teambot.RevisionEQ(rec.Bot.Revision), teambot.LastSeqEQ(rec.LastSequence)).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func findBotDiskMemory(ctx context.Context, db botDB, key string) (*store.MemorySearchResult, error) {
	if _, err := uuid.Parse(key); err != nil {
		return nil, store.ErrBotInvalid
	}
	after := uuid.Nil
	for {
		bots, err := db.ListBots(ctx, 200, after)
		if err != nil {
			return nil, err
		}
		for _, b := range bots {
			cat, err := readBotDiskMemory(ctx, db, b.Bot.ID)
			if err != nil {
				return nil, err
			}
			for _, e := range cat.Entries {
				if e.Row.ID == key {
					r := e.Row
					return &r, nil
				}
			}
		}
		if len(bots) < 200 {
			return nil, nil
		}
		after = bots[len(bots)-1].Bot.ID
	}
}
func insertBotDiskMemory(ctx context.Context, db botDB, id, sid, actor uuid.UUID, content, category string, metadata map[string]any, now time.Time) error {
	botID, err := db.CorrectionSessionBot(ctx, sid)
	if err != nil {
		return err
	}
	cat, err := readBotDiskMemory(ctx, db, botID)
	if err != nil {
		return err
	}
	for _, e := range cat.Entries {
		if e.Row.ID == id.String() {
			return store.ErrBotConflict
		}
	}
	attribution, err := store.DecodeMemoryAttribution(metadata)
	if err != nil {
		return err
	}
	row := store.MemorySearchResult{ID: id.String(), Snippet: content, Category: category, SessionID: sid.String(), CreatedBy: actor.String(), CreatedAt: now.UTC().Format(time.RFC3339), Attribution: attribution}
	rec, err := db.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	row.Scope = string(rec.Bot.Scope)
	row.Path, _ = metadata["source_path"].(string)
	cat.Entries = append(cat.Entries, botDiskMemoryEntry{Row: row, Metadata: metadata})
	return writeBotDiskMemory(ctx, db, cat)
}
func changeBotDiskMemory(ctx context.Context, db botDB, botID uuid.UUID, previous store.MemorySearchResult, change func(*botDiskMemoryEntry) error) error {
	cat, err := readBotDiskMemory(ctx, db, botID)
	if err != nil {
		return err
	}
	for i := range cat.Entries {
		e := &cat.Entries[i]
		if e.Row.ID != previous.ID {
			continue
		}
		if store.MemoryContentFingerprint(e.Row) != store.MemoryContentFingerprint(previous) || !reflect.DeepEqual(e.Row.Attribution, previous.Attribution) {
			return store.ErrBotConflict
		}
		if err := change(e); err != nil {
			return err
		}
		return writeBotDiskMemory(ctx, db, cat)
	}
	return store.ErrBotNotFound
}

// Only Bot services may mutate. The generic interface is read-only and cannot
// accidentally bypass a lease, reviewed-source fingerprint or membership check.
type botDiskMemoryStore struct {
	repo  *botRepository
	id    store.BotID
	owner any
}

func (s *personalBotStore) ValidateBotMemoryStore(memory store.MemoryStore, id store.BotID) error {
	if memory == nil {
		return nil
	}
	switch m := memory.(type) {
	case *personalMemoryStore:
		if m != nil && m.client == s.client {
			return nil
		}
	case *botDiskMemoryStore:
		if m != nil && m.repo != nil && m.owner == s.client && m.id == id && memoryBackendIdentity(m.repo.db) == s.content {
			return nil
		}
	}
	return store.ErrBotUnsupported
}
func (s *teamBotStore) ValidateBotMemoryStore(memory store.MemoryStore, id store.BotID) error {
	if memory == nil {
		return nil
	}
	switch m := memory.(type) {
	case *teamMemoryStore:
		if m != nil && m.client == s.client {
			return nil
		}
	case *botDiskMemoryStore:
		if m != nil && m.repo != nil && m.owner == s.client && m.id == id && memoryBackendIdentity(m.repo.db) == s.content {
			return nil
		}
	}
	return store.ErrBotUnsupported
}

func (s *personalBotStore) BotMemoryStore(id store.BotID) (store.MemoryStore, error) {
	return &botDiskMemoryStore{repo: s.repository(), id: id, owner: s.client}, nil
}
func (s *teamBotStore) BotMemoryStore(id store.BotID) (store.MemoryStore, error) {
	return &botDiskMemoryStore{repo: s.repository(), id: id, owner: s.client}, nil
}
func (m *botDiskMemoryStore) all(ctx context.Context) ([]store.MemorySearchResult, error) {
	if _, err := m.repo.Get(ctx, m.id); err != nil {
		return nil, err
	}
	cat, err := readBotDiskMemory(ctx, m.repo.db, m.id)
	if err != nil {
		return nil, err
	}
	out := make([]store.MemorySearchResult, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		if e.Row.Attribution != nil && strings.HasPrefix(e.Row.Attribution.SupersededBy, "deleted:") {
			continue
		}
		out = append(out, e.Row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}
func (m *botDiskMemoryStore) Get(ctx context.Context, id string) (*store.MemorySearchResult, error) {
	rows, err := m.all(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, nil
}
func (m *botDiskMemoryStore) List(ctx context.Context, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	rows, err := m.all(ctx)
	if err != nil {
		return nil, err
	}
	var sids []string
	for _, row := range rows {
		sids = append(sids, row.SessionID)
	}
	return m.ListBySessions(ctx, sids, category, limit, offset)
}
func (m *botDiskMemoryStore) ListBySession(ctx context.Context, sid string) ([]store.MemorySearchResult, error) {
	return m.ListBySessions(ctx, []string{sid}, "", 0, 0)
}
func (m *botDiskMemoryStore) ListBySessions(ctx context.Context, sids []string, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	if _, err := botMemorySessionIDs(sids); err != nil {
		return nil, err
	}
	rows, err := m.all(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, s := range sids {
		allowed[s] = true
	}
	out := []store.MemorySearchResult{}
	for _, r := range rows {
		if !allowed[r.SessionID] || category != "" && r.Category != category {
			continue
		}
		out = append(out, r)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return []store.MemorySearchResult{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *botDiskMemoryStore) Search(ctx context.Context, q string, n int, min float64) ([]store.MemorySearchResult, error) {
	return m.SearchByCategory(ctx, q, n, min, "")
}
func (m *botDiskMemoryStore) SearchByCategory(ctx context.Context, q string, n int, min float64, category string) ([]store.MemorySearchResult, error) {
	rows, err := m.all(ctx)
	if err != nil {
		return nil, err
	}
	var sids []string
	for _, row := range rows {
		sids = append(sids, row.SessionID)
	}
	return m.SearchBySessions(ctx, sids, q, n, min, category)
}
func (m *botDiskMemoryStore) SearchBySessions(ctx context.Context, sids []string, q string, n int, min float64, category string) ([]store.MemorySearchResult, error) {
	return m.search(ctx, sids, nil, q, n, min, category)
}
func (m *botDiskMemoryStore) SearchAttributed(ctx context.Context, sids []string, f store.MemoryAttributionFilter, q string, n int, min float64, category string) ([]store.MemorySearchResult, error) {
	if f.BotID != m.id {
		return nil, store.ErrBotForbidden
	}
	return m.search(ctx, sids, &f, q, n, min, category)
}
func (m *botDiskMemoryStore) search(ctx context.Context, sids []string, f *store.MemoryAttributionFilter, q string, n int, min float64, category string) ([]store.MemorySearchResult, error) {
	rows, err := m.ListBySessions(ctx, sids, category, 0, 0)
	if err != nil {
		return nil, err
	}
	out := []store.MemorySearchResult{}
	terms := strings.Fields(strings.ToLower(q))
	for _, r := range rows {
		a := r.Attribution
		if f == nil && !store.IsCurrentBotMemory(r) {
			continue
		}
		if f != nil && (a == nil || !f.IncludeSuperseded && a.SupersededBy != "" || f.GoalID != uuid.Nil && a.GoalID != f.GoalID || f.RunID != uuid.Nil && a.RunID != f.RunID) {
			continue
		}
		if err := validateMemoryAttribution(ctx, m.repo.db, r.SessionID, a); err != nil {
			return nil, err
		}
		hits := 0
		for _, t := range terms {
			if strings.Contains(strings.ToLower(r.Snippet), t) {
				hits++
			}
		}
		if len(terms) > 0 {
			r.Score = float64(hits) / float64(len(terms))
		} else {
			r.Score = 1
		}
		if r.Score > 0 && r.Score >= min {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	current, err := m.ListBySessions(ctx, sids, category, 0, 0)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, rows) {
		return nil, store.ErrBotConflict
	}
	return out, nil
}
func (*botDiskMemoryStore) Add(context.Context, store.MemoryEntry) error {
	return store.ErrBotUnsupported
}
func (*botDiskMemoryStore) Update(context.Context, string, string, string) error {
	return store.ErrBotUnsupported
}
func (*botDiskMemoryStore) Delete(context.Context, string) error { return store.ErrBotUnsupported }
func (*botDiskMemoryStore) Count() int                           { return 0 }
func (*botDiskMemoryStore) Close() error                         { return nil }
