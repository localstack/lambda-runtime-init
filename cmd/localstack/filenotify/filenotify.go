// This package is adapted from https://github.com/gohugoio/hugo/tree/master/watcher/filenotify, Apache-2.0 License.

// Package filenotify provides a mechanism for watching file(s) for changes.
// Generally leans on fsnotify, but provides a poll-based notifier which fsnotify does not support.
// These are wrapped up in a common interface so that either can be used interchangeably in your code.
//
// This package is adapted from https://github.com/moby/moby/tree/master/pkg/filenotify, Apache-2.0 License.
// Hopefully this can be replaced with an external package sometime in the future, see https://github.com/fsnotify/fsnotify/issues/9
package filenotify

import (
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher is an interface for implementing file notification watchers
type FileWatcher interface {
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Add(name string) error
	Remove(name string) error
	Close() error
}

func shouldUseEventWatcher() bool {
	// Whether to use an event watcher or polling mechanism
	var utsname unix.Utsname
	err := unix.Uname(&utsname)
	release := strings.TrimRight(string(utsname.Release[:]), "\x00")
	log.Println("Release detected: ", release)
	// cheap check if we are in Docker desktop for Windows (WSL2) or not.
	// We could also inspect the mounts, but that would be more complicated and needs more parsing
	return err == nil && !(strings.Contains(release, "WSL2"))
}

// New tries to use a fs-event watcher, and falls back to the poller if there is an error
func New(interval time.Duration, fileWatcherStrategy string) (FileWatcher, error) {
	if fileWatcherStrategy != "" {
		log.Debugln("Forced usage of filewatcher strategy: ", fileWatcherStrategy)
		if fileWatcherStrategy == "event" {
			if watcher, err := NewEventWatcher(); err == nil {
				return watcher, nil
			} else {
				log.Fatalln("Event based filewatcher is selected, but unable to start. Please try setting the filewatcher to polling. Error: ", err)
			}
		} else if fileWatcherStrategy == "polling" {
			return NewPollingWatcher(interval), nil
		} else {
			log.Fatalf("Invalid filewatcher strategy %s. Only event and polling are allowed.\n", fileWatcherStrategy)
		}
	}
	if shouldUseEventWatcher() {
		if watcher, err := NewEventWatcher(); err == nil {
			log.Debugln("Using event based filewatcher (autodetected)")
			return watcher, nil
		}
	}
	log.Debugln("Using polling based filewatcher (autodetected)")
	return NewPollingWatcher(interval), nil
}

// NewPollingWatcher returns a poll-based file watcher
func NewPollingWatcher(interval time.Duration) FileWatcher {
	return &filePoller{
		interval: interval,
		done:     make(chan struct{}),
		events:   make(chan fsnotify.Event),
		errors:   make(chan error),
	}
}

// NewEventWatcher returns a fs-event based file watcher
func NewEventWatcher() (FileWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsNotifyWatcher{watcher}, nil
}
