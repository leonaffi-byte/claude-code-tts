// internal/telegram/receiver_test.go
package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetUpdates_ParsesMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/getUpdates") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":[
			{"update_id":10,"message":{"chat":{"id":42},"text":"hello"}},
			{"update_id":11,"message":{"chat":{"id":42},"voice":{"file_id":"AwAC123"}}}
		]}`)
	}))
	defer srv.Close()

	r := NewReceiver("tok").WithBaseURL(srv.URL)
	ups, err := r.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 2 {
		t.Fatalf("len = %d, want 2", len(ups))
	}
	if ups[0].Message.Text != "hello" || ups[0].Message.Chat.ID != 42 {
		t.Errorf("msg0 = %+v", ups[0].Message)
	}
	if ups[1].Message.Voice == nil || ups[1].Message.Voice.FileID != "AwAC123" {
		t.Errorf("msg1 voice = %+v", ups[1].Message.Voice)
	}
}

func TestDownloadVoice_GetFileThenDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"voice/file_1.oga"}}`)
		case strings.Contains(r.URL.Path, "/file/bot"):
			_, _ = io.WriteString(w, "OGG-BYTES")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	r := NewReceiver("tok").WithBaseURL(srv.URL)
	data, err := r.DownloadVoice(context.Background(), "AwAC123")
	if err != nil {
		t.Fatalf("DownloadVoice: %v", err)
	}
	if string(data) != "OGG-BYTES" {
		t.Errorf("data = %q", string(data))
	}
}
