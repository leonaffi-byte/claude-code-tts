// internal/stt/stt_test.go
package stt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranscribe_PostsMultipartAndReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("content-type = %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("model") != "gpt-4o-mini-transcribe" {
			t.Errorf("model = %s", r.FormValue("model"))
		}
		_, _ = io.WriteString(w, "shalom world")
	}))
	defer srv.Close()

	c := New("test-key").WithBaseURL(srv.URL)
	got, err := c.Transcribe(context.Background(), []byte("OggS-fake"), "gpt-4o-mini-transcribe", "he")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "shalom world" {
		t.Errorf("got %q", got)
	}
}

func TestTranslate_PostsChatAndReturnsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
	}))
	defer srv.Close()

	c := New("test-key").WithBaseURL(srv.URL)
	got, err := c.Translate(context.Background(), "shalom world", "English")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestTranscribe_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "bad key")
	}))
	defer srv.Close()
	c := New("test-key").WithBaseURL(srv.URL)
	if _, err := c.Transcribe(context.Background(), []byte("x"), "m", ""); err == nil {
		t.Fatal("expected error on 401")
	}
}
