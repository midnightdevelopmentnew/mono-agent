package gemini

import (
	"context"
	"testing"
)

func TestGeminiConstants(t *testing.T) {
	b := &GeminiBot{}
	if b.Platform() != "GEMINI" {
		t.Errorf("Platform() = %q, want GEMINI", b.Platform())
	}
	if b.ResolveURL("anything") != "https://gemini.google.com" {
		t.Error("ResolveURL should always return the Gemini base URL")
	}
	if b.ExtractUsername("anything") != "gemini-user" {
		t.Error("ExtractUsername should return the placeholder")
	}
	if b.SearchURL("x") != "" {
		t.Error("SearchURL is not applicable and should be empty")
	}
}

func TestGeminiSendMessageUnsupported(t *testing.T) {
	b := &GeminiBot{}
	if err := b.SendMessage(context.Background(), nil, "u", "m"); err == nil {
		t.Error("SendMessage should return an error (unsupported)")
	}
}

func TestAlreadyDownloaded(t *testing.T) {
	b := &GeminiBot{}
	imgA := []byte("first image bytes")
	imgB := []byte("second image bytes")

	if b.alreadyDownloaded(imgA) {
		t.Fatal("first sighting of an image reported as already downloaded")
	}
	if !b.alreadyDownloaded(imgA) {
		t.Error("re-extracting the same image was not detected as a duplicate")
	}
	if b.alreadyDownloaded(imgB) {
		t.Error("a different image was reported as a duplicate")
	}
}
