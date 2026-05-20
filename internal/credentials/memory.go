package credentials

import (
	"sort"
	"time"
)

type MemoryStore struct {
	Items map[string]Item
	Now   func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{Items: map[string]Item{}, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *MemoryStore) Get(key string) (Secret, error) {
	if _, _, err := SplitKey(key); err != nil {
		return Secret{}, err
	}
	item, ok := s.Items[key]
	if !ok {
		return Secret{}, ErrNotFound
	}
	return Secret{Key: key, Value: item.Value, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}, nil
}

func (s *MemoryStore) Set(key string, value string) error {
	if _, _, err := SplitKey(key); err != nil {
		return err
	}
	now := s.now()
	item := s.Items[key]
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	item.Value = value
	s.Items[key] = item
	return nil
}

func (s *MemoryStore) Delete(key string) error {
	if _, _, err := SplitKey(key); err != nil {
		return err
	}
	if _, ok := s.Items[key]; !ok {
		return ErrNotFound
	}
	delete(s.Items, key)
	return nil
}

func (s *MemoryStore) List() ([]Metadata, error) {
	out := make([]Metadata, 0, len(s.Items))
	for key, item := range s.Items {
		provider, name, err := SplitKey(key)
		if err != nil {
			return nil, err
		}
		out = append(out, Metadata{Key: key, Provider: provider, Name: name, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *MemoryStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
