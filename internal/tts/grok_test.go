package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrokProvider_Synthesize(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte("GROKMP3"))
	}))
	defer srv.Close()

	p := NewGrokProvider("xai-test", "en")
	p.baseURL = srv.URL

	got, err := p.Synthesize(context.Background(), Request{Text: "hi", Voice: "leo", Speed: 1.3})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(got.Data) != "GROKMP3" || got.Format != "mp3" {
		t.Errorf("got %q/%q", got.Data, got.Format)
	}
	if gotAuth != "Bearer xai-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotPath != "/v1/tts" {
		t.Errorf("path = %q, want /v1/tts", gotPath)
	}
	if gotBody["text"] != "hi" || gotBody["voice_id"] != "leo" || gotBody["language"] != "en" {
		t.Errorf("body = %+v", gotBody)
	}
	if gotBody["speed"] != 1.3 {
		t.Errorf("speed = %v, want 1.3", gotBody["speed"])
	}
	of, ok := gotBody["output_format"].(map[string]any)
	if !ok || of["codec"] != "mp3" {
		t.Errorf("output_format = %v, want codec mp3", gotBody["output_format"])
	}
}

func TestGrokProvider_Metadata(t *testing.T) {
	p := NewGrokProvider("k", "")
	if p.Name() != "grok" || p.DefaultFormat() != "mp3" {
		t.Errorf("metadata wrong: %q/%q", p.Name(), p.DefaultFormat())
	}
	if len(p.Voices()) != 5 {
		t.Errorf("voices = %v", p.Voices())
	}
}
