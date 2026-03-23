package rules

import (
	"log"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches a directory for .arb file changes and hot-reloads the engine.
type Watcher struct {
	engine  *Engine
	watcher *fsnotify.Watcher
	done    chan struct{}
}

// NewWatcher creates and starts a file watcher for the given directory.
// On any Write or Create event for a .arb file, the corresponding domain is
// reloaded. On compilation failure the previous compiled version is kept.
func NewWatcher(engine *Engine, dir string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, err
	}

	w := &Watcher{
		engine:  engine,
		watcher: fw,
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
				name := filepath.Base(event.Name)
				if strings.HasSuffix(name, ".arb") {
					domain := name[:len(name)-4]
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
