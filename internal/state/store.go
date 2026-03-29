package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ytclone/internal/model"
)

type diskState struct {
	Records []model.UploadRecord `json:"records"`
}

const defaultFlushThreshold = 25

// Store persists upload records for idempotent reruns.
// Writes are buffered and flushed after every flushThreshold upserts
// or when Flush is called explicitly, avoiding O(n) disk writes per update.
type Store struct {
	path           string
	mu             sync.Mutex
	records        map[string]model.UploadRecord
	dirty          int
	flushThreshold int
}

func New(path string, legacyUploadedFile string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("state file path is required")
	}

	s := &Store{
		path:           path,
		records:        make(map[string]model.UploadRecord),
		flushThreshold: defaultFlushThreshold,
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	if len(s.records) == 0 && strings.TrimSpace(legacyUploadedFile) != "" {
		if err := s.loadLegacy(legacyUploadedFile); err != nil {
			return nil, err
		}
		if len(s.records) > 0 {
			if err := s.persistLocked(); err != nil {
				return nil, err
			}
		}
	}

	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read state file: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}

	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}

	for _, record := range disk.Records {
		if strings.TrimSpace(record.SourceVideoID) == "" {
			continue
		}
		s.records[record.SourceVideoID] = record
	}
	return nil
}

func (s *Store) loadLegacy(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open legacy uploaded file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	now := time.Now().UTC()
	for scanner.Scan() {
		sourceID := strings.TrimSpace(scanner.Text())
		if sourceID == "" {
			continue
		}
		s.records[sourceID] = model.UploadRecord{
			SourceVideoID: sourceID,
			UploadedAt:    now,
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan legacy uploaded file: %w", err)
	}
	return nil
}

func (s *Store) Snapshot() map[string]model.UploadRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]model.UploadRecord, len(s.records))
	for k, v := range s.records {
		out[k] = v
	}
	return out
}

func (s *Store) Upsert(record model.UploadRecord) error {
	if strings.TrimSpace(record.SourceVideoID) == "" {
		return fmt.Errorf("source video id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.SourceVideoID] = record
	s.dirty++

	if s.dirty >= s.flushThreshold {
		return s.persistLocked()
	}
	return nil
}

// Flush writes any buffered changes to disk. Callers should invoke this
// after a batch of Upsert calls completes (e.g. at the end of a sync run).
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty == 0 {
		return nil
	}
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	dir := filepath.Dir(s.path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create state directory: %w", err)
		}
	}

	keys := make([]string, 0, len(s.records))
	for sourceID := range s.records {
		keys = append(keys, sourceID)
	}
	sort.Strings(keys)

	disk := diskState{Records: make([]model.UploadRecord, 0, len(keys))}
	for _, sourceID := range keys {
		disk.Records = append(disk.Records, s.records[sourceID])
	}

	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	s.dirty = 0
	return nil
}
