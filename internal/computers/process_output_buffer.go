package computers

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const processOutputSegmentSize = 32 << 10

var (
	errProcessOutputBufferLimit = errors.New("command output exceeded its pending output limit")
	errProcessOutputTotalLimit  = errors.New("command output exceeded the Daemon's pending output limit")
)

// processOutputBudget bounds retained segment capacity across a Daemon. It
// counts memory and temporary files together, rather than lifetime output.
type processOutputBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

func newProcessOutputBudget(limit int64) *processOutputBudget {
	return &processOutputBudget{limit: limit}
}

func (b *processOutputBudget) reserve(want int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	amount := min(want, max(int64(0), b.limit-b.used))
	b.used += amount
	return amount
}

func (b *processOutputBudget) release(amount int64) {
	b.mu.Lock()
	b.used -= amount
	b.mu.Unlock()
}

type processOutputBufferConfig struct {
	Directory   string
	MemoryLimit int64
	BufferLimit int64
}

type processOutputSegment struct {
	data     []byte
	path     string
	capacity int64
	size     int64
	offset   int64
}

// processOutputBuffer drains its source independently of a network consumer.
// Small pending output stays in memory; overflow uses closed, private temporary
// files. Consumed memory segments remain reusable up to MemoryLimit and continue
// counting against quotas until Close. Quotas reserve whole segments, bounding
// tiny-write metadata as well as bytes. The one 32 KiB source-read scratch buffer
// is outside those quotas.
//
// Local file IO is serialized under mu; no network write or source Read holds
// that lock. A stalled local filesystem can still encounter the underlying
// process backend's output-close deadline: this does not change that contract.
type processOutputBuffer struct {
	source io.ReadCloser
	config processOutputBufferConfig
	budget *processOutputBudget

	mu             sync.Mutex
	ready          *sync.Cond
	segments       []*processOutputSegment
	freeMemory     []*processOutputSegment
	reserved       int64
	memoryReserved int64
	drained        bool
	closed         bool
	err            error
	readErr        error
	done           chan struct{}
	failed         chan struct{}
	closeDone      chan struct{}
	sourceOnce     sync.Once
	sourceCloseErr error
	closeOnce      sync.Once
	closeErr       error
}

// A successful constructor owns source and starts draining immediately. On a
// validation error source remains the caller's responsibility. Directory must
// be an owned Daemon temporary-file directory, never a command-selected path.
func newProcessOutputBuffer(source io.ReadCloser, config processOutputBufferConfig, budget *processOutputBudget) (*processOutputBuffer, error) {
	if source == nil || budget == nil || budget.limit <= 0 || config.BufferLimit <= 0 ||
		config.MemoryLimit < 0 || config.MemoryLimit > config.BufferLimit ||
		config.MemoryLimit < config.BufferLimit && config.Directory == "" {
		return nil, errors.New("invalid command output buffer configuration")
	}
	b := &processOutputBuffer{source: source, config: config, budget: budget,
		done: make(chan struct{}), failed: make(chan struct{}), closeDone: make(chan struct{})}
	b.ready = sync.NewCond(&b.mu)
	go b.drain()
	return b, nil
}

// Done closes when source draining has stopped. Failed closes on the first real
// collection/storage error, including a later spool-read error. Closed closes
// after Close has joined draining and attempted all temporary-file cleanup.
func (b *processOutputBuffer) Done() <-chan struct{}   { return b.done }
func (b *processOutputBuffer) Failed() <-chan struct{} { return b.failed }
func (b *processOutputBuffer) Closed() <-chan struct{} { return b.closeDone }
func (b *processOutputBuffer) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *processOutputBuffer) failLocked(err error) {
	if err != nil && b.err == nil {
		b.err = err
		close(b.failed)
	}
	b.ready.Broadcast()
}

func (b *processOutputBuffer) closeSource() error {
	b.sourceOnce.Do(func() {
		b.sourceCloseErr = b.source.Close()
		if errors.Is(b.sourceCloseErr, os.ErrClosed) || errors.Is(b.sourceCloseErr, io.ErrClosedPipe) {
			b.sourceCloseErr = nil
		}
	})
	return b.sourceCloseErr
}

func (b *processOutputBuffer) drain() {
	var terminal error
	defer func() {
		if recovered := recover(); recovered != nil {
			terminal = fmt.Errorf("command output reader failed: %v", recovered)
		}
		b.mu.Lock()
		if !b.closed {
			b.failLocked(terminal)
		}
		b.drained = true
		b.ready.Broadcast()
		b.mu.Unlock()
		_ = b.closeSource()
		close(b.done)
	}()
	buffer := make([]byte, processOutputSegmentSize)
	emptyReads := 0
	for {
		n, err := b.source.Read(buffer)
		if n < 0 || n > len(buffer) {
			terminal = errors.New("command output reader returned an invalid byte count")
			break
		}
		if n > 0 {
			emptyReads = 0
			if terminal = b.append(buffer[:n]); terminal != nil {
				break
			}
		} else if err == nil {
			emptyReads++
			if emptyReads >= 100 {
				terminal = io.ErrNoProgress
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				terminal = fmt.Errorf("read command output: %w", err)
			}
			break
		}
	}
}

func (b *processOutputBuffer) segmentLocked() (*processOutputSegment, error) {
	if n := len(b.segments); n > 0 && b.segments[n-1].size < b.segments[n-1].capacity {
		return b.segments[n-1], nil
	}
	if n := len(b.freeMemory); n > 0 {
		segment := b.freeMemory[n-1]
		b.freeMemory[n-1] = nil
		b.freeMemory = b.freeMemory[:n-1]
		b.segments = append(b.segments, segment)
		return segment, nil
	}
	capacity := min(int64(processOutputSegmentSize), b.config.BufferLimit-b.reserved)
	if capacity <= 0 {
		return nil, errProcessOutputBufferLimit
	}
	memory := b.memoryReserved < b.config.MemoryLimit
	if memory {
		capacity = min(capacity, b.config.MemoryLimit-b.memoryReserved)
	}
	capacity = b.budget.reserve(capacity)
	if capacity == 0 {
		return nil, errProcessOutputTotalLimit
	}
	segment := &processOutputSegment{capacity: capacity}
	b.reserved += capacity
	if memory {
		segment.data = make([]byte, 0, int(capacity))
		b.memoryReserved += capacity
	} else {
		file, err := os.CreateTemp(b.config.Directory, ".astral-process-output-*")
		if err != nil {
			b.reserved -= capacity
			b.budget.release(capacity)
			return nil, fmt.Errorf("create command output spool: %w", err)
		}
		segment.path = file.Name()
		err = errors.Join(file.Chmod(0600), file.Close())
		// Retain even an empty segment on failure so Close owns its cleanup and
		// its quota cannot be released while an undeletable file still exists.
		b.segments = append(b.segments, segment)
		if err != nil {
			return nil, fmt.Errorf("prepare command output spool: %w", err)
		}
		return segment, nil
	}
	b.segments = append(b.segments, segment)
	return segment, nil
}

func (b *processOutputBuffer) append(data []byte) (err error) {
	b.mu.Lock()
	defer func() {
		if !b.closed {
			b.failLocked(err)
		}
		b.mu.Unlock()
	}()
	if b.closed {
		return io.ErrClosedPipe
	}
	if b.err != nil {
		return b.err
	}
	for len(data) > 0 {
		segment, err := b.segmentLocked()
		if err != nil {
			return err
		}
		n := min(len(data), int(segment.capacity-segment.size))
		if segment.path == "" {
			segment.data = append(segment.data, data[:n]...)
			segment.size += int64(n)
		} else {
			file, err := os.OpenFile(segment.path, os.O_WRONLY, 0)
			if err != nil {
				return fmt.Errorf("open command output spool for writing: %w", err)
			}
			written, writeErr := file.WriteAt(data[:n], segment.size)
			segment.size += int64(written)
			closeErr := file.Close()
			if written != n && writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			if err := errors.Join(writeErr, closeErr); err != nil {
				return fmt.Errorf("write command output spool: %w", err)
			}
		}
		data = data[n:]
	}
	b.ready.Broadcast()
	return nil
}

// releaseSegmentLocked only returns storage quota after deletion succeeds.
func (b *processOutputBuffer) releaseSegmentLocked(segment *processOutputSegment) error {
	if segment.path != "" {
		if err := os.Remove(segment.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove command output spool: %w", err)
		}
	} else {
		b.memoryReserved -= segment.capacity
	}
	b.reserved -= segment.capacity
	b.budget.release(segment.capacity)
	segment.data = nil
	return nil
}

func (b *processOutputBuffer) consumeSegmentLocked(segment *processOutputSegment) error {
	if segment.path != "" {
		return b.releaseSegmentLocked(segment)
	}
	segment.data = segment.data[:0]
	segment.size, segment.offset = 0, 0
	b.freeMemory = append(b.freeMemory, segment)
	return nil
}

func (b *processOutputBuffer) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	for !b.closed && b.readErr == nil && len(b.segments) == 0 && !b.drained {
		b.ready.Wait()
	}
	if b.closed {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if b.readErr != nil {
		err := b.readErr
		b.mu.Unlock()
		return 0, err
	}
	for len(b.segments) > 0 {
		segment := b.segments[0]
		var n int
		var err error
		if segment.offset < segment.size {
			target := data[:min(len(data), int(segment.size-segment.offset))]
			if segment.path == "" {
				n = copy(target, segment.data[int(segment.offset):])
			} else {
				var file *os.File
				file, err = os.Open(segment.path)
				if err == nil {
					n, err = file.ReadAt(target, segment.offset)
					if errors.Is(err, io.EOF) {
						err = io.ErrUnexpectedEOF
					}
					err = errors.Join(err, file.Close())
				}
				if err != nil {
					err = fmt.Errorf("read command output spool: %w", err)
				}
			}
			segment.offset += int64(n)
		}
		if err == nil && segment.offset == segment.size {
			err = b.consumeSegmentLocked(segment)
			if err == nil {
				b.segments[0] = nil
				if len(b.segments) == 1 {
					b.segments = b.segments[:0]
				} else {
					b.segments = b.segments[1:]
				}
			}
		}
		if err != nil {
			b.readErr = err
			b.failLocked(err)
			b.mu.Unlock()
			_ = b.closeSource()
			return n, err
		}
		if n > 0 {
			b.mu.Unlock()
			return n, nil
		}
	}
	err := b.err
	b.mu.Unlock()
	if err == nil {
		err = io.EOF
	}
	return 0, err
}

func (b *processOutputBuffer) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.ready.Broadcast()
		b.mu.Unlock()
		sourceErr := b.closeSource()
		<-b.done
		b.mu.Lock()
		var cleanupErr error
		for _, segment := range b.segments {
			cleanupErr = errors.Join(cleanupErr, b.releaseSegmentLocked(segment))
		}
		for _, segment := range b.freeMemory {
			cleanupErr = errors.Join(cleanupErr, b.releaseSegmentLocked(segment))
		}
		b.segments = nil
		b.freeMemory = nil
		b.closeErr = errors.Join(sourceErr, cleanupErr)
		b.mu.Unlock()
		close(b.closeDone)
	})
	return b.closeErr
}
