package clienthub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
)

func TestOfflineHostFailsBeforeRequestingApproval(t *testing.T) {
	pairRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/p2p/devices/register":
			json.NewEncoder(w).Encode(map[string]any{"id": "controller", "connectionId": "123456", "token": "test"})
		case "/p2p/devices/resolve":
			json.NewEncoder(w).Encode(map[string]any{"online": false, "device": map[string]any{"id": "host", "connectionId": "654321"}})
		case "/p2p/pairings/request":
			pairRequests++
			http.Error(w, "must not request approval", 500)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var stages []string
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = connectivity.WithProgress(ctx, func(p connectivity.Progress) { stages = append(stages, p.Stage) })
	_, err = h.Connect(ctx, server.URL, "654321", "", "", "Pixel", false)
	if err == nil || !strings.Contains(err.Error(), "离线") || pairRequests != 0 {
		t.Fatalf("offline connection: %v requests=%d", err, pairRequests)
	}
	if strings.Join(stages, ",") != "service,resolve" {
		t.Fatalf("wrong progress %v", stages)
	}
}

func TestGrantWithoutSuccessfulConnectionDoesNotEnableAutoReconnect(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	saved := Saved{ID: "host", Key: "approved-key", Expires: time.Now().Add(time.Hour).Unix()}
	if err := h.DB.SaveEnvironment("host", saved); err != nil {
		t.Fatal(err)
	}
	devices, err := h.List()
	if err != nil {
		t.Fatal(err)
	}
	if devices[0]["autoReconnect"] != false {
		t.Fatal("approval alone enabled automatic retries")
	}
	saved.HasConnected = true
	if err := h.DB.SaveEnvironment("host", saved); err != nil {
		t.Fatal(err)
	}
	devices, err = h.List()
	if err != nil {
		t.Fatal(err)
	}
	if devices[0]["autoReconnect"] != true {
		t.Fatal("successful connection cannot recover automatically")
	}
}
