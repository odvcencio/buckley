package modelprofile

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Store is the domain port for immutable, versioned empirical profile facts.
type Store interface {
	Put(ctx context.Context, profile Profile) error
	Get(ctx context.Context, modelID, version string) (Profile, bool, error)
	Latest(ctx context.Context, modelID string) (Profile, bool, error)
	List(ctx context.Context, modelID string) ([]Profile, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	profiles map[string]map[string]Profile
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{profiles: make(map[string]map[string]Profile)}
}

func (s *MemoryStore) Put(ctx context.Context, profile Profile) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("profile store is unavailable")
	}
	profile = profile.Normalize()
	if err := profile.Validate(); err != nil {
		return err
	}
	digest, err := profile.Digest()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = make(map[string]map[string]Profile)
	}
	versions := s.profiles[profile.ModelID]
	if versions == nil {
		versions = make(map[string]Profile)
		s.profiles[profile.ModelID] = versions
	}
	if existing, ok := versions[profile.Version]; ok {
		existingDigest, digestErr := existing.Digest()
		if digestErr != nil {
			return fmt.Errorf("hash stored profile: %w", digestErr)
		}
		if existingDigest != digest {
			return fmt.Errorf("profile %s version %s already exists with different facts", profile.ModelID, profile.Version)
		}
		return nil
	}
	versions[profile.Version] = profile
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, modelID, version string) (Profile, bool, error) {
	if err := contextErr(ctx); err != nil {
		return Profile{}, false, err
	}
	if s == nil {
		return Profile{}, false, fmt.Errorf("profile store is unavailable")
	}
	s.mu.RLock()
	profile, ok := s.profiles[strings.TrimSpace(modelID)][strings.TrimSpace(version)]
	s.mu.RUnlock()
	return profile, ok, nil
}

func (s *MemoryStore) Latest(ctx context.Context, modelID string) (Profile, bool, error) {
	profiles, err := s.List(ctx, modelID)
	if err != nil || len(profiles) == 0 {
		return Profile{}, false, err
	}
	return profiles[len(profiles)-1], true, nil
}

func (s *MemoryStore) List(ctx context.Context, modelID string) ([]Profile, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("profile store is unavailable")
	}
	s.mu.RLock()
	versions := s.profiles[strings.TrimSpace(modelID)]
	profiles := make([]Profile, 0, len(versions))
	for _, profile := range versions {
		profiles = append(profiles, profile)
	}
	s.mu.RUnlock()
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].MeasuredAt.Equal(profiles[j].MeasuredAt) {
			return profiles[i].Version < profiles[j].Version
		}
		return profiles[i].MeasuredAt.Before(profiles[j].MeasuredAt)
	})
	return profiles, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
