package vlsup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientHealthAuthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "jm" || pass != "local-only" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer srv.Close()

	// httptest 监听 127.0.0.1：base URL 可注入且满足 localhost 策略。
	cli, err := NewClient(ClientOptions{BaseURL: srv.URL, Username: "jm", Password: "local-only"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Health(context.Background()); err != nil {
		t.Fatalf("authorized health must pass: %v", err)
	}
}

func TestClientUnauthorizedReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "jm" || pass != "local-only" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{BaseURL: srv.URL, Username: "jm", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	err = cli.Health(context.Background())
	if err == nil {
		t.Fatal("unauthorized must return error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestClientRejectsNonLoopbackBaseURL(t *testing.T) {
	_, err := NewClient(ClientOptions{BaseURL: "http://10.0.0.8:19441", Username: "jm", Password: "x"})
	if err == nil {
		t.Fatal("non-loopback base URL must fail in production policy")
	}
	if !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("want ErrNotLoopback, got %v", err)
	}
	// 测试注入开关
	cli, err := NewClient(ClientOptions{
		BaseURL:          "http://10.0.0.8:19441",
		Username:         "jm",
		Password:         "x",
		AllowNonLoopback: true,
	})
	if err != nil || cli == nil {
		t.Fatalf("AllowNonLoopback should permit inject for tests: %v", err)
	}
}

func TestClientGetQueryString(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cli, err := NewClient(ClientOptions{BaseURL: srv.URL, Username: "jm", Password: "local-only"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := cli.Get(context.Background(), "/select/logsql/query", url.Values{"query": []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("unexpected body %s", body)
	}
	if gotQuery.Get("query") != "*" {
		t.Fatalf("query not forwarded: %v", gotQuery)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "127.0.0.5"} {
		if !IsLoopbackHost(h) {
			t.Fatalf("%s should be loopback", h)
		}
	}
	for _, h := range []string{"", "0.0.0.0", "example.com", "192.168.1.1"} {
		if IsLoopbackHost(h) {
			t.Fatalf("%s must not be loopback", h)
		}
	}
}

func TestClientPostPartitionAPIUsesPOSTAndBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/partition/list" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "jm" || pass != "secret" {
			t.Error("missing Basic auth")
		}
		_, _ = w.Write([]byte(`["20260923"]`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, Username: "jm", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Post(context.Background(), "/internal/partition/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `["20260923"]` {
		t.Fatalf("unexpected body: %s", body)
	}
}
