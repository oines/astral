package transport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/transport"
)

func TestOversizedProtocolFrameClosesBeforeReadingPayload(t *testing.T) {
	left, right := net.Pipe()
	peer := transport.New(left, nil)
	t.Cleanup(func() { _ = peer.Close(); _ = right.Close() })
	go func() {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], ^uint32(0))
		_, _ = right.Write(prefix[:])
	}()
	select {
	case <-peer.Done():
	case <-time.After(time.Second):
		t.Fatal("peer attempted to read an oversized payload")
	}
}

func pair(t *testing.T, handler transport.Handler) *transport.Peer {
	t.Helper()
	servers := make(chan *transport.Peer, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		p := transport.New(websocket.NetConn(context.Background(), conn, websocket.MessageBinary), handler)
		servers <- p
		<-p.Done()
	}))
	conn, _, err := websocket.Dial(context.Background(), httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.New(websocket.NetConn(context.Background(), conn, websocket.MessageBinary), nil)
	server := <-servers
	t.Cleanup(func() { _ = client.Close(); _ = server.Close(); httpServer.Close() })
	return client
}

func TestBoundedFileTransferDoesNotBlockOtherCalls(t *testing.T) {
	content := bytes.Repeat([]byte("Astral stream data\n"), 100000)
	slowStarted := make(chan struct{})
	client := pair(t, func(ctx context.Context, req transport.Request, out *transport.Responder) error {
		switch req.Method {
		case "slow":
			if err := out.Headers(map[string]int{"size": len(content)}); err != nil {
				return err
			}
			close(slowStarted)
			_, err := out.Write(content)
			return err
		case "echo":
			return out.JSON(json.RawMessage(req.Params))
		case "upload":
			hash := sha256.New()
			if _, err := io.Copy(hash, req.Body); err != nil {
				return err
			}
			return out.JSON(fmt.Sprintf("%x", hash.Sum(nil)))
		default:
			return errors.New("unexpected method")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, download, err := client.Call(ctx, "slow", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Close()
	<-slowStarted
	// Keep download unread. Its sender must exhaust only its own credits.
	var reply string
	if err = client.JSON(ctx, "echo", "unblocked", &reply); err != nil || reply != "unblocked" {
		t.Fatalf("multiplexing blocked: %s %v", reply, err)
	}
	_, uploadReply, err := client.Call(ctx, "upload", nil, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	var gotHash string
	if err = json.NewDecoder(uploadReply).Decode(&gotHash); err != nil {
		t.Fatal(err)
	}
	_ = uploadReply.Close()
	want := fmt.Sprintf("%x", sha256.Sum256(content))
	if gotHash != want {
		t.Fatal("uploaded bytes changed")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, download); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != want {
		t.Fatal("downloaded bytes changed")
	}
}

func TestCancelSettlesRemoteAndConnectionRemainsUsable(t *testing.T) {
	cancelled := make(chan struct{})
	client := pair(t, func(ctx context.Context, req transport.Request, out *transport.Responder) error {
		if req.Method == "wait" {
			if err := out.Headers(nil); err != nil {
				return err
			}
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		}
		return out.JSON("alive")
	})
	ctx, cancel := context.WithCancel(context.Background())
	_, body, err := client.Call(ctx, "wait", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = body.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	_ = body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("remote call did not observe cancellation")
	}
	var value string
	if err = client.JSON(context.Background(), "next", nil, &value); err != nil || value != "alive" {
		t.Fatalf("one cancelled call closed peer: %v", err)
	}
}

func TestConcurrentCallsAndRemoteErrors(t *testing.T) {
	client := pair(t, func(ctx context.Context, req transport.Request, out *transport.Responder) error {
		if req.Method == "error" {
			return &transport.Error{Code: "missing", Message: "not found", Outcome: "not_executed"}
		}
		return out.JSON(json.RawMessage(req.Params))
	})
	var group sync.WaitGroup
	for i := range 40 {
		group.Add(1)
		go func() {
			defer group.Done()
			var got int
			if err := client.JSON(context.Background(), "echo", i, &got); err != nil || got != i {
				t.Errorf("call %d got %d: %v", i, got, err)
			}
		}()
	}
	group.Wait()
	var value any
	err := client.JSON(context.Background(), "error", nil, &value)
	var remote *transport.Error
	if !errors.As(err, &remote) || remote.Outcome != "not_executed" {
		t.Fatalf("error classification lost: %v", err)
	}
}

func TestCloseAfterRemoteSocketHasAlreadyClosed(t *testing.T) {
	drop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-drop
		_ = conn.CloseNow()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := transport.New(websocket.NetConn(context.Background(), conn, websocket.MessageBinary), nil)
	close(drop)
	select {
	case <-peer.Done():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := peer.Close(); err != nil {
		t.Fatalf("closing an already-disconnected peer failed: %v", err)
	}
}
