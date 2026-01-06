package index

import (
	"context"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/storage"
)

// Indexer scans the repository and populates structured metadata for fast lookup.
type Indexer struct {
	store      *storage.Store
	root       string
	ignoreDirs map[string]struct{}
}

// New creates a new Indexer rooted at the provided directory.
func New(store *storage.Store, root string) *Indexer {
	ignore := map[string]struct{}{
		".git":         {},
		"node_modules": {},
		"vendor":       {},
		".buckley":     {},
	}
	return &Indexer{
		store:      store,
		root:       root,
		ignoreDirs: ignore,
	}
}

// IndexStats tracks what happened during an indexing operation.
type IndexStats struct {
	FilesScanned  int
	FilesIndexed  int
	FilesSkipped  int
	FilesDeleted  int
	IsIncremental bool
}

// Scan walks the repository and indexes Go source files.
func (idx *Indexer) Scan(ctx context.Context) error {
	_, err := idx.ScanWithStats(ctx)
	return err
}

// ScanWithStats walks the repository and indexes Go source files, returning stats.
func (idx *Indexer) ScanWithStats(ctx context.Context) (*IndexStats, error) {
	if idx.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}

	stats := &IndexStats{}

	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(idx.root, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			if _, skip := idx.ignoreDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		stats.FilesScanned++
		stats.FilesIndexed++
		return idx.indexGoFile(ctx, relPath, path)
	}

	return stats, filepath.WalkDir(idx.root, walkFn)
}

// IncrementalScan walks the repository and only re-indexes files that have changed.
// It also removes files from the index that no longer exist on disk.
func (idx *Indexer) IncrementalScan(ctx context.Context) (*IndexStats, error) {
	if idx.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}

	stats := &IndexStats{IsIncremental: true}

	// Get existing checksums from database
	existingChecksums, err := idx.store.GetAllFileChecksums(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting existing checksums: %w", err)
	}

	// Track files we see during the walk
	seenFiles := make(map[string]struct{})

	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(idx.root, path)
		if err != nil {
			return err
		}
		// Normalize path separators
		relPath = filepath.ToSlash(relPath)

		if d.IsDir() {
			if _, skip := idx.ignoreDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		stats.FilesScanned++
		seenFiles[relPath] = struct{}{}

		// Check if file has changed by computing current checksum
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(data)
		currentChecksum := fmt.Sprintf("%x", sum[:])

		// Compare with stored checksum
		if storedChecksum, exists := existingChecksums[relPath]; exists && storedChecksum == currentChecksum {
			stats.FilesSkipped++
			return nil
		}

		// File is new or changed, re-index it
		stats.FilesIndexed++
		return idx.indexGoFileWithData(ctx, relPath, path, data)
	}

	if err := filepath.WalkDir(idx.root, walkFn); err != nil {
		return stats, err
	}

	// Clean up files that no longer exist
	for existingPath := range existingChecksums {
		if _, seen := seenFiles[existingPath]; !seen {
			if err := idx.store.DeleteFileRecord(ctx, existingPath); err != nil {
				return stats, fmt.Errorf("deleting stale file %s: %w", existingPath, err)
			}
			stats.FilesDeleted++
		}
	}

	return stats, nil
}

func (idx *Indexer) indexGoFile(ctx context.Context, relPath, absPath string) error {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	return idx.indexGoFileWithData(ctx, relPath, absPath, data)
}

// indexGoFileWithData indexes a Go file using pre-read data (avoids double-read for incremental scan).
func (idx *Indexer) indexGoFileWithData(ctx context.Context, relPath, absPath string, data []byte) error {
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(data)
	rec := &storage.FileRecord{
		Path:      filepath.ToSlash(relPath),
		Checksum:  fmt.Sprintf("%x", sum[:]),
		Language:  "go",
		SizeBytes: info.Size(),
		Summary:   "", // Populated in later iterations (e.g., LLM summary)
		UpdatedAt: info.ModTime(),
	}

	if err := idx.store.UpsertFileRecord(ctx, rec); err != nil {
		return err
	}

	fset := token.NewFileSet()
	fileAST, err := parser.ParseFile(fset, absPath, data, parser.ParseComments)
	if err != nil {
		return err
	}

	symbols := extractSymbols(fset, fileAST, rec.Path)
	if err := idx.store.ReplaceSymbols(ctx, rec.Path, symbols); err != nil {
		return err
	}

	imports := extractImports(fileAST, rec.Path)
	return idx.store.ReplaceImports(ctx, rec.Path, imports)
}

func extractSymbols(fset *token.FileSet, file *ast.File, relPath string) []storage.SymbolRecord {
	var symbols []storage.SymbolRecord

	addSymbol := func(name, kind, signature string, pos, end token.Pos) {
		start := fset.Position(pos)
		finish := fset.Position(end)
		symbols = append(symbols, storage.SymbolRecord{
			FilePath:  relPath,
			Name:      name,
			Kind:      kind,
			Signature: signature,
			StartLine: start.Line,
			EndLine:   finish.Line,
		})
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "function"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
			}
			addSymbol(d.Name.Name, kind, formatFuncSignature(d), d.Pos(), d.End())
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						addSymbol(ts.Name.Name, "type", "", ts.Pos(), ts.End())
					}
				}
			}
		}
	}

	return symbols
}

func extractImports(file *ast.File, relPath string) []storage.ImportRecord {
	imports := make([]storage.ImportRecord, 0, len(file.Imports))
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, storage.ImportRecord{
			FilePath:   relPath,
			ImportPath: path,
		})
	}
	return imports
}

func formatFuncSignature(fn *ast.FuncDecl) string {
	var parts []string
	params := fieldListToString(fn.Type.Params)
	results := fieldListToString(fn.Type.Results)
	parts = append(parts, fmt.Sprintf("func %s(%s)", fn.Name.Name, params))
	if results != "" {
		parts = append(parts, results)
	}
	return strings.Join(parts, " ")
}

func fieldListToString(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}

	var elems []string
	for _, field := range fl.List {
		typeExpr := exprToString(field.Type)
		if len(field.Names) == 0 {
			elems = append(elems, typeExpr)
			continue
		}
		for _, name := range field.Names {
			elems = append(elems, fmt.Sprintf("%s %s", name.Name, typeExpr))
		}
	}
	return strings.Join(elems, ", ")
}

func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + exprToString(e.Sel)
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.FuncType:
		return "func"
	default:
		return ""
	}
}
