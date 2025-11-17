package watcher

import (
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	path    string
	watcher *fsnotify.Watcher
}

func New(path string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := fw.Add(path); err != nil {
		return nil, err
	}

	return &Watcher{
		path:    path,
		watcher: fw,
	}, nil
}

func (w *Watcher) Start() {
	log.Printf("File watcher started on: %s", w.path)

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	if filepath.Base(event.Name) == "docker-compose.yml" ||
	   filepath.Base(event.Name) == "docker-compose.yaml" ||
	   filepath.Base(event.Name) == "service.yml" {

		switch event.Op {
		case fsnotify.Create:
			log.Printf("Detected new file: %s", event.Name)
		case fsnotify.Write:
			log.Printf("Detected file modification: %s", event.Name)
		case fsnotify.Remove:
			log.Printf("Detected file removal: %s", event.Name)
		}
	}
}

func (w *Watcher) Close() error {
	return w.watcher.Close()
}
