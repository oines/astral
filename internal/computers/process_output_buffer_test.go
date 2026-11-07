package computers

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type processOutputTestSource struct {
	io.Reader
	closed  atomic.Bool
	maxRead int
}

type processOutputTestFailureReader struct {
	err  error
	sent bool
}

func (r *processOutputTestFailureReader) Read(data []byte) (int, error) {
	if r.sent {
		return 0, r.err
	}
	r.sent = true
	return copy(data, "synthetic prefix before source failure"), r.err
}

func (s *processOutputTestSource) Read(data []byte) (int, error) {
	if s.maxRead > 0 {
		data = data[:min(len(data), s.maxRead)]
	}
	return s.Reader.Read(data)
}

func (s *processOutputTestSource) Close() error {
	s.closed.Store(true)
	return nil
}

func processOutputTestBuffer(t *testing.T, source io.ReadCloser, memory, limit int64, budget *processOutputBudget) (*processOutputBuffer, string) {
	t.Helper()
	directory := t.TempDir()
	buffer, err := newProcessOutputBuffer(source, processOutputBufferConfig{Directory: directory, MemoryLimit: memory, BufferLimit: limit}, budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := buffer.Close(); err != nil {
			t.Errorf("close synthetic output buffer: %v", err)
		}
	})
	return buffer, directory
}

func processOutputTestWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic output worker did not settle")
	}
}

func processOutputTestUsage(budget *processOutputBudget) int64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

func processOutputTestFiles(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestProcessOutputBufferDrainsBeforeConsumerAndKeepsSmallOutputInMemory(t *testing.T) {
	const text = "synthetic command output\n"
	source := &processOutputTestSource{Reader: strings.NewReader(text)}
	budget := newProcessOutputBudget(4 * processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, source, processOutputSegmentSize, 4*processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	if !source.closed.Load() || buffer.Err() != nil {
		t.Fatalf("source did not drain independently: closed=%t err=%v", source.closed.Load(), buffer.Err())
	}
	if files := processOutputTestFiles(t, directory); len(files) != 0 {
		t.Fatalf("small output unexpectedly created %d spool files", len(files))
	}
	actual, err := io.ReadAll(buffer)
	if err != nil || string(actual) != text {
		t.Fatalf("buffered output %q, %v", actual, err)
	}
	if used := processOutputTestUsage(budget); used != processOutputSegmentSize {
		t.Fatalf("consumed memory did not retain its bounded reusable quota: %d", used)
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if processOutputTestUsage(budget) != 0 {
		t.Fatal("Close retained reusable memory quota")
	}
	if err := buffer.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	processOutputTestWait(t, buffer.Closed())
	select {
	case <-buffer.Failed():
		t.Fatal("normal EOF/Close was reported as an output failure")
	default:
	}
}

func TestProcessOutputBufferSpoolsPrivatelyAndDeletesConsumedSegments(t *testing.T) {
	data := bytes.Repeat([]byte("synthetic output\n"), 8000)
	source := &processOutputTestSource{Reader: bytes.NewReader(data)}
	budget := newProcessOutputBudget(8 * processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, source, processOutputSegmentSize, 8*processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	files := processOutputTestFiles(t, directory)
	if len(files) < 2 || buffer.Err() != nil {
		t.Fatalf("expected spooled output: files=%d err=%v", len(files), buffer.Err())
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("spool is not a private regular file: %s", info.Mode())
		}
	}
	actual, err := io.ReadAll(buffer)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("spooled output differs: actual=%d expected=%d err=%v", len(actual), len(data), err)
	}
	if files := processOutputTestFiles(t, directory); len(files) != 0 || processOutputTestUsage(budget) != processOutputSegmentSize {
		t.Fatalf("consumed spool retained: files=%d quota=%d", len(files), processOutputTestUsage(budget))
	}
}

func TestProcessOutputBufferReusesBoundedMemoryForInteractiveBytes(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	budget := newProcessOutputBudget(2 * processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, reader, processOutputSegmentSize, 2*processOutputSegmentSize, budget)
	var original *processOutputSegment
	var one [1]byte
	for i := range 128 {
		if _, err := writer.Write([]byte("s")); err != nil {
			t.Fatalf("interactive write %d: %v", i, err)
		}
		if _, err := io.ReadFull(buffer, one[:]); err != nil || one[0] != 's' {
			t.Fatalf("interactive read %d: %v", i, err)
		}
		buffer.mu.Lock()
		var segment *processOutputSegment
		if len(buffer.freeMemory) == 1 {
			segment = buffer.freeMemory[0]
		}
		reserved, memory := buffer.reserved, buffer.memoryReserved
		buffer.mu.Unlock()
		if i == 0 {
			original = segment
		}
		if segment == nil || segment != original || reserved != processOutputSegmentSize || memory != processOutputSegmentSize || processOutputTestUsage(budget) != processOutputSegmentSize {
			t.Fatalf("interactive byte %d replaced or exceeded its reusable segment", i)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	processOutputTestWait(t, buffer.Done())
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(processOutputTestFiles(t, directory)) != 0 || processOutputTestUsage(budget) != 0 {
		t.Fatal("closed interactive buffer retained storage or quota")
	}
}

func TestProcessOutputBufferMergesTinyReadsIntoBoundedSegments(t *testing.T) {
	data := bytes.Repeat([]byte("s"), 2000)
	source := &processOutputTestSource{Reader: bytes.NewReader(data), maxRead: 1}
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, _ := processOutputTestBuffer(t, source, processOutputSegmentSize, processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	buffer.mu.Lock()
	segments, retained := len(buffer.segments), buffer.reserved
	buffer.mu.Unlock()
	if segments != 1 || retained != processOutputSegmentSize {
		t.Fatalf("tiny reads created unbounded metadata: segments=%d retained=%d", segments, retained)
	}
	actual, err := io.ReadAll(buffer)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("tiny reads changed output: actual=%d err=%v", len(actual), err)
	}
}

func TestProcessOutputBufferReusesQuotaAcrossLifetimeOutput(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, reader, 0, processOutputSegmentSize, budget)
	data := bytes.Repeat([]byte("synthetic-stream-"), processOutputSegmentSize/17+1)[:processOutputSegmentSize]
	actual := make([]byte, len(data))
	for i := range 20 {
		if _, err := writer.Write(data); err != nil {
			t.Fatalf("write synthetic segment %d: %v", i, err)
		}
		if _, err := io.ReadFull(buffer, actual); err != nil || !bytes.Equal(actual, data) {
			t.Fatalf("read synthetic segment %d: %v", i, err)
		}
		if used := processOutputTestUsage(budget); used != 0 {
			t.Fatalf("consumed segment %d retained quota: %d", i, used)
		}
		if files := processOutputTestFiles(t, directory); len(files) != 0 {
			t.Fatalf("consumed segment %d retained %d files", i, len(files))
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	processOutputTestWait(t, buffer.Done())
	if _, err := buffer.Read(actual); err != io.EOF || buffer.Err() != nil {
		t.Fatalf("lifetime output was incorrectly limited: read=%v buffer=%v", err, buffer.Err())
	}
}

func TestProcessOutputBufferPerProcessLimitPreservesPrefixAndReportsFailure(t *testing.T) {
	data := bytes.Repeat([]byte("s"), 2*processOutputSegmentSize+1)
	source := &processOutputTestSource{Reader: bytes.NewReader(data)}
	budget := newProcessOutputBudget(4 * processOutputSegmentSize)
	buffer, _ := processOutputTestBuffer(t, source, processOutputSegmentSize, 2*processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	processOutputTestWait(t, buffer.Failed())
	actual, err := io.ReadAll(buffer)
	if !errors.Is(err, errProcessOutputBufferLimit) || !errors.Is(buffer.Err(), errProcessOutputBufferLimit) || len(actual) != 2*processOutputSegmentSize || !bytes.Equal(actual, data[:len(actual)]) {
		t.Fatalf("capacity failure lost its known prefix/error: bytes=%d read=%v buffer=%v", len(actual), err, buffer.Err())
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if !source.closed.Load() || processOutputTestUsage(budget) != 0 {
		t.Fatalf("failed source/quota was retained: closed=%t used=%d", source.closed.Load(), processOutputTestUsage(budget))
	}
}

func TestProcessOutputBufferSharesBudgetWithoutWaitingForConsumers(t *testing.T) {
	budget := newProcessOutputBudget(processOutputSegmentSize)
	first, _ := processOutputTestBuffer(t, io.NopCloser(strings.NewReader("synthetic first")), processOutputSegmentSize, processOutputSegmentSize, budget)
	processOutputTestWait(t, first.Done())
	second, _ := processOutputTestBuffer(t, io.NopCloser(strings.NewReader("synthetic second")), processOutputSegmentSize, processOutputSegmentSize, budget)
	processOutputTestWait(t, second.Done())
	if _, err := io.ReadAll(second); !errors.Is(err, errProcessOutputTotalLimit) {
		t.Fatalf("shared limit did not fail explicitly: %v", err)
	}
	if used := processOutputTestUsage(budget); used != processOutputSegmentSize {
		t.Fatalf("shared quota escaped its limit: %d", used)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, _ := processOutputTestBuffer(t, io.NopCloser(strings.NewReader("synthetic third")), processOutputSegmentSize, processOutputSegmentSize, budget)
	processOutputTestWait(t, third.Done())
	if actual, err := io.ReadAll(third); err != nil || string(actual) != "synthetic third" {
		t.Fatalf("released shared quota could not be reused: %q %v", actual, err)
	}
}

func TestProcessOutputBufferSpoolFailureIsNotEOF(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "synthetic-not-a-directory")
	if err := os.WriteFile(directory, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, err := newProcessOutputBuffer(io.NopCloser(strings.NewReader("synthetic disk failure")), processOutputBufferConfig{Directory: directory, BufferLimit: processOutputSegmentSize}, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Close()
	processOutputTestWait(t, buffer.Done())
	if _, err := io.ReadAll(buffer); err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "spool") {
		t.Fatalf("storage failure looked like successful EOF: %v", err)
	}
	if buffer.Err() == nil || processOutputTestUsage(budget) != 0 {
		t.Fatalf("failed spool creation retained quota or lost error: err=%v used=%d", buffer.Err(), processOutputTestUsage(budget))
	}
}

func TestProcessOutputBufferSourceFailureRetainsItsKnownBytes(t *testing.T) {
	failure := errors.New("synthetic source failure")
	source := &processOutputTestSource{Reader: &processOutputTestFailureReader{err: failure}}
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, _ := processOutputTestBuffer(t, source, processOutputSegmentSize, processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	actual, err := io.ReadAll(buffer)
	if string(actual) != "synthetic prefix before source failure" || !errors.Is(err, failure) || !errors.Is(buffer.Err(), failure) {
		t.Fatalf("source failure lost its bytes/error: %q read=%v buffer=%v", actual, err, buffer.Err())
	}
	processOutputTestWait(t, buffer.Failed())
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if !source.closed.Load() || processOutputTestUsage(budget) != 0 {
		t.Fatal("source failure retained its reader or consumed quota")
	}
}

func TestProcessOutputBufferLateSpoolReadFailureSignalsAfterDrain(t *testing.T) {
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, io.NopCloser(strings.NewReader("synthetic retained output")), 0, processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	if buffer.Err() != nil {
		t.Fatal(buffer.Err())
	}
	files := processOutputTestFiles(t, directory)
	if len(files) != 1 {
		t.Fatalf("expected one test-owned spool file, got %d", len(files))
	}
	if err := os.Truncate(filepath.Join(directory, files[0].Name()), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(buffer); !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		t.Fatalf("truncated spool was silently accepted: %v", err)
	}
	processOutputTestWait(t, buffer.Failed())
	if buffer.Err() == nil {
		t.Fatal("late storage error was not observable")
	}
}

func TestProcessOutputBufferCloseJoinsBlockedDrainAndConcurrentReaders(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	budget := newProcessOutputBudget(processOutputSegmentSize)
	buffer, _ := processOutputTestBuffer(t, reader, processOutputSegmentSize, processOutputSegmentSize, budget)
	readDone := make(chan error, 1)
	go func() {
		_, err := buffer.Read(make([]byte, 1))
		readDone <- err
	}()
	var closes sync.WaitGroup
	for range 8 {
		closes.Go(func() {
			if err := buffer.Close(); err != nil {
				t.Errorf("concurrent Close: %v", err)
			}
		})
	}
	closed := make(chan struct{})
	go func() { closes.Wait(); close(closed) }()
	processOutputTestWait(t, closed)
	processOutputTestWait(t, buffer.Done())
	processOutputTestWait(t, buffer.Closed())
	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked read did not observe Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close left its consumer blocked")
	}
	if processOutputTestUsage(budget) != 0 {
		t.Fatal("closed drain retained its quota")
	}
}

func TestProcessOutputBufferCloseDiscardsUnreadSpool(t *testing.T) {
	data := bytes.Repeat([]byte("s"), 2*processOutputSegmentSize)
	budget := newProcessOutputBudget(2 * processOutputSegmentSize)
	buffer, directory := processOutputTestBuffer(t, io.NopCloser(bytes.NewReader(data)), 0, 2*processOutputSegmentSize, budget)
	processOutputTestWait(t, buffer.Done())
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(processOutputTestFiles(t, directory)) != 0 || processOutputTestUsage(budget) != 0 {
		t.Fatal("Close did not release unread output files and quota")
	}
}

func TestProcessOutputBufferRejectsInvalidConfigurationWithoutOwningSource(t *testing.T) {
	source := &processOutputTestSource{Reader: strings.NewReader("synthetic")}
	if _, err := newProcessOutputBuffer(source, processOutputBufferConfig{MemoryLimit: 2, BufferLimit: 1}, newProcessOutputBudget(1)); err == nil {
		t.Fatal("invalid limits were accepted")
	}
	if source.closed.Load() {
		t.Fatal("rejected constructor took ownership of its source")
	}
}
