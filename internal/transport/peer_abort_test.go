package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestResponderAbortUnblocksCreditLimitedResponseAndKeepsPeer(t *testing.T) {
	left, right := net.Pipe()
	exhausted := make(chan *Responder, 1)
	returned := make(chan error, 1)
	server := New(right, func(_ context.Context, request Request, out *Responder) error {
		if request.Method == "next" {
			return out.JSON("still connected")
		}
		if err := out.Headers(nil); err != nil {
			return err
		}
		chunk := bytes.Repeat([]byte("x"), chunkSize)
		for range window {
			if _, err := out.Write(chunk); err != nil {
				return err
			}
		}
		exhausted <- out
		// The client has taken headers but does not consume its body. All output
		// credits are spent, so this write can only finish by cancellation.
		_, err := out.Write(chunk)
		returned <- err
		return err
	})
	client := New(left, nil)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, body, err := client.Call(ctx, "blocked", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var responder *Responder
	select {
	case responder = <-exhausted:
	case <-ctx.Done():
		t.Fatal("response did not exhaust its stream credits")
	}
	select {
	case err := <-returned:
		t.Fatalf("write completed while the client was not reading: %v", err)
	default:
	}
	aborted := errors.New("command API credential revoked")
	responder.Abort(aborted)
	select {
	case err := <-returned:
		if !errors.Is(err, aborted) {
			t.Fatalf("write lost the abort cause: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("abort did not unblock the response write")
	}
	// Draining after revocation consumes only data already sent before Abort,
	// followed by the final RPC error. It must not keep a live stream slot.
	buffer := make([]byte, chunkSize)
	for {
		_, err = body.Read(buffer)
		if err != nil {
			break
		}
	}
	var remote *Error
	if !errors.As(err, &remote) || remote.Message != aborted.Error() {
		t.Fatalf("client did not receive the terminal abort: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []*Peer{client, server} {
		ticker := time.NewTicker(time.Millisecond)
		for {
			peer.mu.Lock()
			remaining := len(peer.streams)
			peer.mu.Unlock()
			if remaining == 0 {
				break
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				ticker.Stop()
				t.Fatalf("aborted RPC retained %d stream slots", remaining)
			}
		}
		ticker.Stop()
	}
	var next string
	if err := client.JSON(ctx, "next", nil, &next); err != nil || next != "still connected" {
		t.Fatalf("abort damaged the peer: response=%q error=%v", next, err)
	}
}

func TestResponderAbortBeforeResponseRejectsHeadersAndAvailableCredits(t *testing.T) {
	left, right := net.Pipe()
	aborted := errors.New("credential was revoked before response")
	checks := make(chan [2]error, 1)
	server := New(right, func(_ context.Context, _ Request, out *Responder) error {
		out.Abort(aborted)
		headerErr := out.Headers(nil)
		_, writeErr := out.Write([]byte("must not be sent"))
		checks <- [2]error{headerErr, writeErr}
		return writeErr
	})
	client := New(left, nil)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, body, err := client.Call(ctx, "revoked", nil, nil)
	if body != nil {
		_ = body.Close()
		t.Fatal("aborted call sent response headers")
	}
	var remote *Error
	if !errors.As(err, &remote) || remote.Message != aborted.Error() {
		t.Fatalf("client did not receive the terminal abort: %v", err)
	}
	select {
	case result := <-checks:
		if !errors.Is(result[0], aborted) || !errors.Is(result[1], aborted) {
			t.Fatalf("aborted response used headers or unspent credits: headers=%v write=%v", result[0], result[1])
		}
	case <-ctx.Done():
		t.Fatal("aborted handler did not return")
	}
}
