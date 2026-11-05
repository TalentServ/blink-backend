package httpapi

import "testing"

func TestStageCodexAttachments(t *testing.T) {
	overlays := map[string]string{}
	images, note, stored := stageCodexAttachments([]codexAttachment{
		{Name: "shot.png", Kind: "image", DataURL: "data:image/png;base64,abc"},
		{Name: "notes.txt", Kind: "file", Content: "hello"},
		{Name: "main.go", Kind: "file_ref", Path: "src/main.go"},
	}, overlays)
	if len(images) != 1 || overlays["attachments/notes.txt"] != "hello" || len(stored) != 3 {
		t.Fatalf("images=%d overlays=%v stored=%d", len(images), overlays, len(stored))
	}
	if note == "" {
		t.Fatal("expected attachment note")
	}
}
