package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendAudio(t *testing.T) {
	var gotPath, gotChat string
	var gotAudio []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		gotChat = r.FormValue("chat_id")
		f, _, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("FormFile audio: %v", err)
		} else {
			buf := make([]byte, 64)
			n, _ := f.Read(buf)
			gotAudio = buf[:n]
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := NewSender("TOKEN123", "555")
	s.baseURL = srv.URL
	if err := s.SendAudio(context.Background(), []byte("MP3DATA"), "hello"); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	if gotPath != "/botTOKEN123/sendAudio" {
		t.Errorf("path = %q, want /botTOKEN123/sendAudio", gotPath)
	}
	if gotChat != "555" {
		t.Errorf("chat_id = %q, want 555", gotChat)
	}
	if string(gotAudio) != "MP3DATA" {
		t.Errorf("audio = %q, want MP3DATA", gotAudio)
	}
}

func TestSendAudio_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	s := NewSender("T", "0")
	s.baseURL = srv.URL
	err := s.SendAudio(context.Background(), []byte("x"), "")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected error containing API description, got %v", err)
	}
}
