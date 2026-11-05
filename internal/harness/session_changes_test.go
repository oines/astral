package harness

import (
	"sync"
	"testing"
)

func TestSessionChangesBroadcastWithoutRetainingClosedReaders(t *testing.T) {
	s := &Service{}
	first, second, other := s.WatchSession("one"), s.WatchSession("one"), s.WatchSession("two")
	defer other.Close()
	for range 1000 {
		s.sessionChanges.notify("one")
	}
	for _, w := range []*SessionWatcher{first, second} {
		select {
		case <-w.C:
		default:
			t.Fatal("one reader consumed another reader's change")
		}
		select {
		case <-w.C:
			t.Fatal("invalidation signals did not coalesce")
		default:
		}
	}
	select {
	case <-other.C:
		t.Fatal("unrelated Session was invalidated")
	default:
	}
	s.sessionChanges.notify("one")
	<-first.C
	<-second.C
	first.Close()
	first.Close()
	second.Close()
	for range 1000 {
		s.WatchSession("discarded").Close()
	}
	if len(s.sessionChanges.watchers) != 1 {
		t.Fatal("closed readers were retained")
	}
	s.sessionChanges.close()
	if _, ok := <-other.C; ok {
		t.Fatal("shutdown did not close the watcher")
	}
	late := s.WatchSession("one")
	defer late.Close()
	if _, ok := <-late.C; ok {
		t.Fatal("a reader registered after shutdown remained open")
	}
}

func TestSessionChangesConcurrentRegistrationAndNotification(t *testing.T) {
	s := &Service{}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 500 {
				w := s.WatchSession("one")
				s.sessionChanges.notify("one")
				w.Close()
			}
		})
	}
	workers.Wait()
	if len(s.sessionChanges.watchers) != 0 {
		t.Fatal("concurrent readers were retained")
	}
}

func TestDirectoryChangesDoNotInvalidateAnUnchangedSessionView(t *testing.T) {
	s := &Service{}
	directory, view := s.WatchSession(""), s.WatchSession("one")
	defer directory.Close()
	defer view.Close()
	// A Core commit invalidates the directory, while the existing Core Event
	// subscription owns the individual view's ordered updates.
	s.sessionChanges.notify("")
	<-directory.C
	select {
	case <-view.C:
		t.Fatal("directory invalidation duplicated a Session's Core events")
	default:
	}
	s.sessionChanges.notify("one")
	<-directory.C
	<-view.C
}
