package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withYoutubeTestServer points youtubeAPIBase at an httptest server for the
// duration of the test and restores it afterward.
func withYoutubeTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	orig := youtubeAPIBase
	youtubeAPIBase = srv.URL
	t.Cleanup(func() {
		srv.Close()
		youtubeAPIBase = orig
	})
	return srv
}

func TestYouTubeNode_SearchVideos(t *testing.T) {
	withYoutubeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer token-123" {
			t.Errorf("Authorization = %q, want Bearer token-123", auth)
		}
		q := r.URL.Query()
		if q.Get("part") != "snippet" {
			t.Errorf("part = %q, want snippet", q.Get("part"))
		}
		if q.Get("type") != "video" {
			t.Errorf("type = %q, want video", q.Get("type"))
		}
		if q.Get("q") != "golang tutorials" {
			t.Errorf("q = %q, want golang tutorials", q.Get("q"))
		}
		if q.Get("maxResults") != "5" {
			t.Errorf("maxResults = %q, want 5", q.Get("maxResults"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"id": map[string]interface{}{"videoId": "abc123"},
					"snippet": map[string]interface{}{
						"title":        "Golang Tutorial",
						"description":  "Learn Go",
						"channelTitle": "GoChannel",
						"publishedAt":  "2026-01-01T00:00:00Z",
					},
				},
			},
		})
	})

	n := &YouTubeNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "search_videos",
		"access_token": "token-123",
		"query":        "golang tutorials",
		"max_results":  5,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := mainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["video_id"] != "abc123" {
		t.Errorf("video_id = %v, want abc123", items[0].JSON["video_id"])
	}
	if items[0].JSON["title"] != "Golang Tutorial" {
		t.Errorf("title = %v, want Golang Tutorial", items[0].JSON["title"])
	}
	if items[0].JSON["channel_title"] != "GoChannel" {
		t.Errorf("channel_title = %v, want GoChannel", items[0].JSON["channel_title"])
	}
}

func TestYouTubeNode_SearchVideosDefaultMaxResults(t *testing.T) {
	withYoutubeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("maxResults"); got != "10" {
			t.Errorf("maxResults = %q, want 10 (default)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": []interface{}{}})
	})

	n := &YouTubeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "search_videos",
		"access_token": "token-123",
		"query":        "golang",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
}

func TestYouTubeNode_SearchVideosMissingQuery(t *testing.T) {
	n := &YouTubeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "search_videos",
		"access_token": "token-123",
	})
	if err == nil {
		t.Fatal("expected error when query is missing, got nil")
	}
}

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
