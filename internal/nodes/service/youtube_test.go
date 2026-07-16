package service

import "testing"

func TestYoutubeParseVideoStats(t *testing.T) {
	raw := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"id": "abc123",
				"statistics": map[string]interface{}{
					"viewCount":    "1000",
					"likeCount":    "50",
					"commentCount": "10",
				},
			},
		},
	}
	result, err := youtubeParseVideoStats(raw)
	if err != nil {
		t.Fatalf("youtubeParseVideoStats: %v", err)
	}
	if result["view_count"] != "1000" {
		t.Errorf("view_count = %v, want 1000", result["view_count"])
	}
	if result["like_count"] != "50" {
		t.Errorf("like_count = %v, want 50", result["like_count"])
	}
}

func TestYoutubeParseVideoStatsNoItems(t *testing.T) {
	raw := map[string]interface{}{"items": []interface{}{}}
	_, err := youtubeParseVideoStats(raw)
	if err == nil {
		t.Fatal("expected error when no video is found, got nil")
	}
}

func TestYoutubeBuildUploadMetadata(t *testing.T) {
	meta := youtubeBuildUploadMetadata("Title", "Description", []string{"tag1", "tag2"}, "22", "public")
	snippet := meta["snippet"].(map[string]interface{})
	if snippet["title"] != "Title" {
		t.Errorf("title = %v, want Title", snippet["title"])
	}
	tags := snippet["tags"].([]string)
	if len(tags) != 2 || tags[0] != "tag1" {
		t.Errorf("tags = %v, want [tag1 tag2]", tags)
	}
	status := meta["status"].(map[string]interface{})
	if status["privacyStatus"] != "public" {
		t.Errorf("privacyStatus = %v, want public", status["privacyStatus"])
	}
}
