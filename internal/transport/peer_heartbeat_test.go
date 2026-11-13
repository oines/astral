package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type countedConn struct {
	net.Conn
	writes atomic.Int32
	pings  atomic.Int32
}

func (c *countedConn) Write(data []byte) (int, error) {
	c.writes.Add(1)
	n, err := c.Conn.Write(data)
	if n == len(data) && len(data) > 4 {
		var f frame
		if json.Unmarshal(data, &f) == nil && f.Kind == "ping" {
			c.pings.Add(1)
		}
	}
	return n, err
}

func readTestFrame(conn net.Conn) (frame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > 128<<10 {
		return frame{}, errors.New("invalid test protocol frame size")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil {
		return frame{}, err
	}
	var f frame
	err := json.Unmarshal(raw, &f)
	return f, err
}

func writeTestFrame(conn net.Conn, f frame) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if _, err = conn.Write(prefix[:]); err != nil {
		return err
	}
	_, err = conn.Write(raw)
	return err
}

func TestHeartbeatsBypassFullRPCSlotsAndStreamCredits(t *testing.T) {
	left, right := net.Pipe()
	clientConn := &countedConn{Conn: left}
	serverConn := &countedConn{Conn: right}
	var handled atomic.Int32
	server := newPeer(serverConn, func(_ context.Context, req Request, out *Responder) error {
		handled.Add(1)
		if req.Method == "blocked-output" {
			if err := out.Headers(nil); err != nil {
				return err
			}
			_, err := out.Write(bytes.Repeat([]byte("x"), chunkSize*(window+1)))
			return err
		}
		return out.JSON("alive")
	}, 10*time.Millisecond, 250*time.Millisecond)
	client := newPeer(clientConn, nil, 10*time.Millisecond, 250*time.Millisecond)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, output, err := client.Call(ctx, "blocked-output", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	var held []*stream
	for _, peer := range []*Peer{client, server} {
		for i := range maxCalls - 1 {
			s, err := peer.makeStream(fmt.Sprintf("held-%d", i), false)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, s)
		}
	}
	// Both peers send heartbeats over an unbuffered connection while all RPC
	// slots are occupied and one response has exhausted its output credits.
	select {
	case <-client.Done():
		t.Fatal("client heartbeat failed while RPC streams were busy")
	case <-server.Done():
		t.Fatal("server heartbeat failed while RPC streams were busy")
	case <-time.After(600 * time.Millisecond):
	}
	if clientConn.pings.Load() < 2 || serverConn.pings.Load() < 2 {
		t.Fatal("both peers did not complete repeated heartbeat rounds")
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("heartbeat dispatched an RPC handler: %d calls", got)
	}
	for _, s := range held {
		s.peer.remove(s, context.Canceled)
	}
	var reply string
	if err := client.JSON(ctx, "next", nil, &reply); err != nil || reply != "alive" {
		t.Fatalf("connection failed after heartbeats: %q, %v", reply, err)
	}
}

func TestHeartbeatTimeoutSettlesActiveCalls(t *testing.T) {
	for _, replyWrongID := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong_pong_%t", replyWrongID), func(t *testing.T) {
			left, right := net.Pipe()
			peer := newPeer(left, nil, 10*time.Millisecond, 50*time.Millisecond)
			t.Cleanup(func() { _ = peer.Close(); _ = right.Close() })
			opened := make(chan struct{})
			go func() {
				for {
					f, err := readTestFrame(right)
					if err != nil {
						return
					}
					if f.Kind == "open" {
						close(opened)
					}
					if f.Kind == "ping" && replyWrongID {
						if writeTestFrame(right, frame{ID: "unrelated", Kind: "pong"}) != nil {
							return
						}
					}
				}
			}()
			result := make(chan error, 1)
			go func() { result <- peer.JSON(context.Background(), "wait", nil, nil) }()
			select {
			case <-opened:
			case <-time.After(time.Second):
				t.Fatal("RPC was not opened")
			}
			select {
			case <-peer.Done():
			case <-time.After(time.Second):
				t.Fatal("unanswered heartbeat did not close the peer")
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrDisconnected) {
					t.Fatalf("active call error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("disconnected call did not settle")
			}
		})
	}
}

func TestHeartbeatTimeoutIncludesBlockedWrite(t *testing.T) {
	left, right := net.Pipe()
	peer := newPeer(left, nil, 10*time.Millisecond, 50*time.Millisecond)
	t.Cleanup(func() { _ = peer.Close(); _ = right.Close() })
	// Nothing reads from right, so even the first ping cannot be written.
	select {
	case <-peer.Done():
	case <-time.After(time.Second):
		t.Fatal("blocked ping write postponed the heartbeat timeout")
	}
}

func newHeartbeatTestPeer(t *testing.T) (*Peer, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	peer := newPeer(local, nil, time.Hour, heartbeatTimeout)
	t.Cleanup(func() { _ = peer.Close(); _ = remote.Close() })
	if err := remote.SetDeadline(time.Now().Add(heartbeatMaxWait + time.Second)); err != nil {
		t.Fatal(err)
	}
	return peer, remote
}

func runTestHeartbeat(peer *Peer) <-chan bool {
	result := make(chan bool, 1)
	go func() { result <- peer.heartbeat(heartbeatTimeout) }()
	return result
}

func requireHeartbeatFrame(t *testing.T, conn net.Conn, kind string) frame {
	t.Helper()
	f, err := readTestFrame(conn)
	if err != nil || f.Kind != kind {
		t.Fatalf("read %s: got kind=%q err=%v", kind, f.Kind, err)
	}
	return f
}

func requireHeartbeatWrite(t *testing.T, conn net.Conn, f frame) {
	t.Helper()
	if err := writeTestFrame(conn, f); err != nil {
		t.Fatalf("write %s: %v", f.Kind, err)
	}
	synctest.Wait()
}

func requireHeartbeatPending(t *testing.T, peer *Peer) {
	t.Helper()
	synctest.Wait()
	select {
	case <-peer.Done():
		t.Fatal("peer closed while the heartbeat was pending")
	default:
	}
}

func requireHeartbeatResult(t *testing.T, result <-chan bool, want bool) {
	t.Helper()
	synctest.Wait()
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("heartbeat success=%t, want %t", got, want)
		}
	default:
		t.Fatal("heartbeat did not finish")
	}
}

func TestHeartbeatAllowsLateCreditWhilePingWriteIsBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, remote := newHeartbeatTestPeer(t)
		s, err := peer.makeStream("finished-file", true)
		if err != nil {
			t.Fatal(err)
		}
		sent := make(chan error, 1)
		go func() {
			_, err := s.write([]byte("file"))
			sent <- err
		}()
		data := requireHeartbeatFrame(t, remote, "data")
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		peer.remove(s, nil)

		result := runTestHeartbeat(peer)
		time.Sleep(heartbeatTimeout * 3 / 4)
		// The receiver can acknowledge the final bytes after the sender
		// has already retired the stream. Hold the ping on the write side
		// while that credit arrives in the other direction.
		requireHeartbeatWrite(t, remote, frame{ID: data.ID, Kind: "credit"})
		time.Sleep(heartbeatTimeout / 2)
		requireHeartbeatPending(t, peer)
		ping := requireHeartbeatFrame(t, remote, "ping")
		requireHeartbeatWrite(t, remote, frame{ID: ping.ID, Kind: "pong"})
		requireHeartbeatResult(t, result, true)
	})
}

func TestHeartbeatExpiresAfterLastValidTransferFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, remote := newHeartbeatTestPeer(t)
		result := runTestHeartbeat(peer)
		ping := requireHeartbeatFrame(t, remote, "ping")
		s, err := peer.makeStream("file", false)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(heartbeatTimeout * 3 / 4)
		requireHeartbeatWrite(t, remote, frame{ID: s.id, Kind: "data", Data: []byte("file")})
		time.Sleep(heartbeatTimeout * 3 / 4)
		requireHeartbeatPending(t, peer)
		requireHeartbeatWrite(t, remote, frame{ID: ping.ID + "-wrong", Kind: "pong"})
		requireHeartbeatWrite(t, remote, frame{ID: "retired-file", Kind: "credit", Data: []byte("invalid credit")})
		time.Sleep(heartbeatTimeout / 4)
		requireHeartbeatResult(t, result, false)
	})
}

func TestHeartbeatCapsWaitWithContinuousTransferFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, remote := newHeartbeatTestPeer(t)
		result := runTestHeartbeat(peer)
		requireHeartbeatFrame(t, remote, "ping")
		s, err := peer.makeStream("file", false)
		if err != nil {
			t.Fatal(err)
		}
		const step = heartbeatTimeout / 2
		for elapsed := step; elapsed < heartbeatMaxWait; elapsed += step {
			time.Sleep(step)
			requireHeartbeatPending(t, peer)
			requireHeartbeatWrite(t, remote, frame{ID: s.id, Kind: "data", Data: []byte("file")})
			<-s.data
		}
		time.Sleep(step)
		requireHeartbeatResult(t, result, false)
	})
}

type cancelOnMarshal struct{ cancel context.CancelFunc }

func (p cancelOnMarshal) MarshalJSON() ([]byte, error) {
	p.cancel()
	return []byte("null"), nil
}

func TestCanceledCallDoesNotDispatchOpen(t *testing.T) {
	for _, duringMarshal := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_marshal_%t", duringMarshal), func(t *testing.T) {
			left, right := net.Pipe()
			conn := &countedConn{Conn: left}
			peer := newPeer(conn, nil, time.Hour, time.Hour)
			t.Cleanup(func() { _ = peer.Close(); _ = right.Close() })
			go func() {
				for {
					if _, err := readTestFrame(right); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var params any
			if duringMarshal {
				params = cancelOnMarshal{cancel: cancel}
			} else {
				cancel()
			}
			if err := peer.JSON(ctx, "must-not-start", params, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled call error = %v", err)
			}
			if conn.writes.Load() != 0 {
				t.Fatal("canceled call dispatched protocol frames")
			}
		})
	}
}

type cancelFirstWriteConn struct {
	net.Conn
	once   sync.Once
	cancel context.CancelFunc
}

func (c *cancelFirstWriteConn) Write(data []byte) (int, error) {
	c.once.Do(c.cancel)
	return c.Conn.Write(data)
}

func TestCancellationDuringOpenFollowsOpenOnWire(t *testing.T) {
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := newPeer(&cancelFirstWriteConn{Conn: left, cancel: cancel}, nil, time.Hour, time.Hour)
	t.Cleanup(func() { _ = peer.Close(); _ = right.Close() })
	frames := make(chan []frame, 1)
	go func() {
		var received []frame
		for {
			f, err := readTestFrame(right)
			if err != nil {
				return
			}
			received = append(received, f)
			if f.Kind == "cancel" {
				frames <- received
				return
			}
		}
	}()
	if err := peer.JSON(ctx, "starting", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start error = %v", err)
	}
	select {
	case received := <-frames:
		if len(received) < 2 || received[0].Kind != "open" || received[len(received)-1].ID != received[0].ID {
			t.Fatalf("cancel did not follow its open: %+v", received)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was not sent after open completed")
	}
}
