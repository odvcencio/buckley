package index

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/storage"
)

// Service exposes structured queries against the stored index.
type Service struct {
	store *storage.Store
}

// NewService builds an index query service.
func NewService(store *storage.Store) *Service {
	if store == nil {
		return nil
	}
	return &Service{store: store}
}

// LookupFiles returns file metadata by query/path glob.
func (s *Service) LookupFiles(ctx context.Context, query, path string, limit int) ([]storage.FileRecord, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	return s.store.SearchFiles(ctx, query, path, limit)
}

// LookupSymbols returns symbols by name/path glob.
func (s *Service) LookupSymbols(ctx context.Context, symbol, path string, limit int) ([]storage.SymbolRecord, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	return s.store.SearchSymbols(ctx, symbol, path, limit)
}
