package terminal

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestTerminalVisibilityPreservesAttachmentAndCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reads := make(chan Params, 4)
	canceled := make(chan struct{})
	release := make(chan struct{})
	events := make(chan error, 1)
	calls := 0
	snapshot := Snapshot{ID: "terminal", Attachment: "original", Sequence: 7, Size: Size{Cols: 80, Rows: 24}, Data: []byte("cached")}
	conn, err := AttachConn(ctx, func(request context.Context, method string, p Params) (Snapshot, error) {
		switch method {
		case "terminal.attach":
			return snapshot, nil
		case "terminal.read":
			reads <- p
			calls++
			switch calls {
			case 1:
				<-request.Done()
				close(canceled)
				return Snapshot{}, request.Err()
			case 2:
				return Snapshot{}, errors.New("终端已有读取请求")
			case 3:
				<-release
				return Snapshot{ID: snapshot.ID, Attachment: snapshot.Attachment, Sequence: 8, Size: snapshot.Size, Data: []byte("next")}, nil
			}
		case "terminal.detach":
			return Snapshot{}, nil
		}
		return Snapshot{}, errors.New("unexpected terminal operation")
	}, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Bind(func(Size) {}, func(err error, _ bool) { events <- err })
	cached := make([]byte, len(snapshot.Data))
	if _, err := io.ReadFull(conn, cached); err != nil || string(cached) != "cached" {
		t.Fatalf("initial frame = %q, %v", cached, err)
	}
	result := make(chan string, 1)
	go func() {
		next := make([]byte, 4)
		_, err := io.ReadFull(conn, next)
		if err != nil {
			result <- err.Error()
		} else {
			result <- string(next)
		}
	}()
	checkRead := func() {
		t.Helper()
		select {
		case r := <-reads:
			if r.Attachment != "original" || r.After != 7 {
				t.Fatalf("changed attachment or cursor: %+v", r)
			}
		case <-ctx.Done():
			t.Fatal("terminal did not read", ctx.Err())
		}
	}
	checkRead()
	conn.SetVisible(false)
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("hiding did not cancel the read")
	}
	select {
	case r := <-reads:
		t.Fatalf("hidden terminal continued reading: %+v", r)
	case <-time.After(75 * time.Millisecond):
	}
	conn.SetVisible(true)
	checkRead()
	checkRead()
	close(release)
	select {
	case got := <-result:
		if got != "next" {
			t.Fatalf("resumed output = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("no resumed output", ctx.Err())
	}
	select {
	case e := <-events:
		t.Fatal("switching was reported as a failure", e)
	default:
	}
}
