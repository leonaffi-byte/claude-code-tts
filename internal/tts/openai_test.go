package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIProvider_Synthesize_Opus(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OGGOPUS"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", "tts-1")
	p.baseURL = srv.URL
	got, err := p.Synthesize(context.Background(), Request{Text: "hi", Voice: "alloy", Format: "opus"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if got.Format != "opus" {
		t.Errorf("format = %q, want opus", got.Format)
	}
	if gotBody["response_format"] != "opus" {
		t.Errorf("response_format = %v, want opus", gotBody["response_format"])
	}
}

func TestOpenAIProvider_Synthesize(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FAKEMP3"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", "tts-1")
	p.baseURL = srv.URL // unexported field; same-package test

	got, err := p.Synthesize(context.Background(), Request{Text: "hello", Voice: "onyx", Speed: 1.2})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(got.Data) != "FAKEMP3" {
		t.Errorf("data = %q, want FAKEMP3", got.Data)
	}
	if got.Format != "mp3" {
		t.Errorf("format = %q, want mp3", got.Format)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
	if gotPath != "/v1/audio/speech" {
		t.Errorf("path = %q, want /v1/audio/speech", gotPath)
	}
	if gotBody["model"] != "tts-1" || gotBody["voice"] != "onyx" || gotBody["input"] != "hello" {
		t.Errorf("body = %+v, want model=tts-1 voice=onyx input=hello", gotBody)
	}
	if gotBody["speed"] != 1.2 {
		t.Errorf("speed = %v, want 1.2", gotBody["speed"])
	}
}

func TestOpenAIProvider_Synthesize_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad key"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", "tts-1")
	p.baseURL = srv.URL
	if _, err := p.Synthesize(context.Background(), Request{Text: "x", Voice: "alloy"}); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestOpenAIProvider_Metadata(t *testing.T) {
	p := NewOpenAIProvider("k", "")
	if p.Name() != "openai" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.DefaultFormat() != "mp3" {
		t.Errorf("DefaultFormat = %q", p.DefaultFormat())
	}
	if len(p.Voices()) != 6 {
		t.Errorf("Voices = %v", p.Voices())
	}
}
