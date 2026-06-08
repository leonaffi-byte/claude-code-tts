package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 8)
	wp.Start()
	t.Cleanup(wp.Stop)
	return &Server{workerPool: wp}
}

func speakReq(args map[string]any) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Arguments = args
	return r
}

func TestNew(t *testing.T) {
	s, err := New() // back-compat default path must succeed with NO API key set
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Shutdown()
	if s.mcpServer == nil || s.workerPool == nil {
		t.Fatal("server not fully constructed")
	}
}

func TestHandleSpeak_QueuesWithProfile(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleSpeak(context.Background(), speakReq(map[string]any{
		"text": "hello", "profile": "default", "speed": 1.2,
	}))
	if err != nil {
		t.Fatalf("handleSpeak: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected error result")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "job-") || !strings.Contains(text, "default") {
		t.Errorf("result = %q", text)
	}
}

func TestHandleSpeak_QueuesWithProvider(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleSpeak(context.Background(), speakReq(map[string]any{
		"text": "hi", "provider": "grok", "voice": "leo",
	}))
	if err != nil || res.IsError {
		t.Fatalf("handleSpeak err=%v", err)
	}
	if !strings.Contains(res.Content[0].(mcp.TextContent).Text, "provider:grok") {
		t.Errorf("result = %q", res.Content[0].(mcp.TextContent).Text)
	}
}

func TestHandleSpeak_MissingText(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{}))
	if !res.IsError {
		t.Error("expected error for missing text")
	}
}

func TestHandleSpeak_EmptyText(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{"text": ""}))
	if !res.IsError {
		t.Error("expected error for empty text")
	}
}

func TestHandleSpeak_TextTooLong(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{"text": strings.Repeat("a", 4097)}))
	if !res.IsError {
		t.Error("expected error for too-long text")
	}
}

func TestHandleStatus(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleStatus(context.Background(), speakReq(nil))
	if err != nil || res.IsError {
		t.Fatalf("handleStatus failed: %v", err)
	}
	if !strings.Contains(res.Content[0].(mcp.TextContent).Text, "worker_count") {
		t.Error("status missing worker_count")
	}
}

func TestHandleSetOutput(t *testing.T) {
	dir := t.TempDir()
	store := voicemode.NewStore(filepath.Join(dir, "state.json"))
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 4).WithMode(store)
	wp.Start()
	t.Cleanup(wp.Stop)
	s := &Server{workerPool: wp, modeStore: store}

	res, err := s.handleSetOutput(context.Background(), speakReq(map[string]any{"mode": "phone"}))
	if err != nil || res.IsError {
		t.Fatalf("handleSetOutput: %v / %+v", err, res)
	}
	if store.Get() != voicemode.Phone {
		t.Errorf("mode = %q, want phone", store.Get())
	}

	// tts_status must reflect the new mode (json.MarshalIndent renders "key": "value").
	st, _ := s.handleStatus(context.Background(), speakReq(nil))
	if !strings.Contains(st.Content[0].(mcp.TextContent).Text, `"voice_mode": "phone"`) {
		t.Errorf("tts_status missing voice_mode=phone:\n%s", st.Content[0].(mcp.TextContent).Text)
	}
	// This server has no telegram sender wired, so the status must report it as not configured.
	if !strings.Contains(st.Content[0].(mcp.TextContent).Text, `"telegram_configured": false`) {
		t.Errorf("tts_status missing telegram_configured=false:\n%s", st.Content[0].(mcp.TextContent).Text)
	}

	bad, _ := s.handleSetOutput(context.Background(), speakReq(map[string]any{"mode": "loud"}))
	if !bad.IsError {
		t.Error("expected error for invalid mode")
	}
}
