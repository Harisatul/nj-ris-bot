package embedding

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOllamaEmbed768(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[` + strings.Trim(strings.Repeat("0,", 767), ",") + `,0]]}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api/embed", "", "embeddinggemma-2:440m", 768)
	got, err := c.Embed(context.Background(), "remember this")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 768 {
		t.Fatalf("dimensions=%d", len(got))
	}
}

func TestOllamaRejectsUnexpectedDimensions(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"embeddings":[[0,1]]}`)) }))
	defer server.Close()
	c := New(server.URL, "", "test", 768)
	if _, err := c.Embed(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "expected 768") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOllamaTimeoutHonorsContext(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer server.Close()
	c := New(server.URL, "", "test", 768)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Embed(ctx, "query"); err == nil {
		t.Fatal("expected timeout error")
	}
}

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}
