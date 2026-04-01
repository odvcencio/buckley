package rules

import (
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches a directory (recursively) for .arb file changes and
// hot-reloads the engine.
type Watcher struct {
	engine  *Engine
	watcher *fsnotify.Watcher
	rootDir string
	done    chan struct{}
}

// NewWatcher creates and starts a file watcher for the given directory.
// All subdirectories are watched recursively.
// On any Write or Create event for a .arb file, the corresponding domain is
// reloaded. On compilation failure the previous compiled version is kept.
func NewWatcher(engine *Engine, dir string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return fw.Add(path)
		}
		return nil
	})
	if err != nil {
		fw.Close()
		return nil, err
	}

	w := &Watcher{
		engine:  engine,
		watcher: fw,
		rootDir: dir,
		done:    make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

// Close stops the watcher goroutine and releases resources.
func (w *Watcher) Close() error {
	err := w.watcher.Close()
	<-w.done
	return err
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if strings.HasSuffix(event.Name, ".arb") {
					rel, err := filepath.Rel(w.rootDir, event.Name)
					if err != nil {
						continue
					}
					domain := strings.TrimSuffix(rel, ".arb")
					if err := w.engine.Reload(domain); err != nil {
						log.Printf("rules: hot reload failed for domain %q, keeping previous version: %v", domain, err)
					} else {
						log.Printf("rules: reloaded domain %q", domain)
					}
				}
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("rules: watcher error: %v", err)
		}
	}
}
