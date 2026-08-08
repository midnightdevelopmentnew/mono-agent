package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"monoagent/internal/workflow"
)

// YouTubeNode uploads videos and reads stats/comments via the YouTube Data API v3.
// Type: "service.youtube"
//
// Config fields:
//
//	"operation"        (string, required): "upload_video" | "get_video_stats" | "list_comments" | "reply_to_comment" | "search_videos"
//	"access_token"     (string, required): OAuth2 access token
//	"title"            (string, required for upload_video)
//	"description"      (string): video description
//	"tags"             ([]interface{} of string): video tags
//	"category_id"      (string): YouTube category ID, default "22" (People & Blogs)
//	"privacy_status"   (string): "public" (default) | "unlisted" | "private"
//	"video_file_path"  (string, required for upload_video): local path to the video file
//	"video_id"         (string, required for get_video_stats/list_comments)
//	"comment_id"       (string, required for reply_to_comment)
//	"text"             (string, required for reply_to_comment)
//	"query"            (string, required for search_videos)
//	"max_results"      (int, optional for search_videos, default 10)
type YouTubeNode struct{}

func (n *YouTubeNode) Type() string { return "service.youtube" }

const youtubeUploadURL = "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status"

// youtubeAPIBase is declared as a var (not const) so tests can point it at
// an httptest server.
var youtubeAPIBase = "https://www.googleapis.com/youtube/v3"

func (n *YouTubeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("youtube: access_token is required")
	}
	operation := strVal(config, "operation")

	switch operation {
	case "upload_video":
		title := strVal(config, "title")
		videoPath := strVal(config, "video_file_path")
		if title == "" || videoPath == "" {
			return nil, fmt.Errorf("youtube: title and video_file_path are required for upload_video")
		}
		categoryID := strVal(config, "category_id")
		if categoryID == "" {
			categoryID = "22"
		}
		privacyStatus := strVal(config, "privacy_status")
		if privacyStatus == "" {
			privacyStatus = "public"
		}
		metadata := youtubeBuildUploadMetadata(title, strVal(config, "description"), strSliceVal(config, "tags"), categoryID, privacyStatus)

		result, err := youtubeUploadVideo(ctx, accessToken, metadata, videoPath)
		if err != nil {
			return nil, fmt.Errorf("youtube upload_video: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "get_video_stats":
		videoID := strVal(config, "video_id")
		if videoID == "" {
			return nil, fmt.Errorf("youtube: video_id is required for get_video_stats")
		}
		endpoint := fmt.Sprintf("%s/videos?part=statistics&id=%s", youtubeAPIBase, videoID)
		raw, err := apiRequest(ctx, "GET", endpoint, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("youtube get_video_stats: %w", err)
		}
		result, err := youtubeParseVideoStats(raw)
		if err != nil {
			return nil, fmt.Errorf("youtube get_video_stats: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "list_comments":
		videoID := strVal(config, "video_id")
		if videoID == "" {
			return nil, fmt.Errorf("youtube: video_id is required for list_comments")
		}
		endpoint := fmt.Sprintf("%s/commentThreads?part=snippet&videoId=%s&maxResults=100", youtubeAPIBase, videoID)
		raw, err := apiRequest(ctx, "GET", endpoint, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("youtube list_comments: %w", err)
		}
		items, err := youtubeParseCommentThreads(raw)
		if err != nil {
			return nil, fmt.Errorf("youtube list_comments: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	case "search_videos":
		query := strVal(config, "query")
		if query == "" {
			return nil, fmt.Errorf("youtube: query is required for search_videos")
		}
		maxResults := intVal(config, "max_results")
		if maxResults <= 0 {
			maxResults = 10
		}
		endpoint := fmt.Sprintf("%s/search?part=snippet&type=video&q=%s&maxResults=%d", youtubeAPIBase, url.QueryEscape(query), maxResults)
		raw, err := apiRequest(ctx, "GET", endpoint, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("youtube search_videos: %w", err)
		}
		items, err := youtubeParseSearchResults(raw)
		if err != nil {
			return nil, fmt.Errorf("youtube search_videos: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	case "reply_to_comment":
		commentID := strVal(config, "comment_id")
		text := strVal(config, "text")
		if commentID == "" || text == "" {
			return nil, fmt.Errorf("youtube: comment_id and text are required for reply_to_comment")
		}
		body := map[string]interface{}{
			"snippet": map[string]interface{}{
				"parentId":     commentID,
				"textOriginal": text,
			},
		}
		endpoint := youtubeAPIBase + "/comments?part=snippet"
		raw, err := apiRequest(ctx, "POST", endpoint, accessToken, body)
		if err != nil {
			return nil, fmt.Errorf("youtube reply_to_comment: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(raw)}}}, nil

	default:
		return nil, fmt.Errorf("youtube: unsupported operation %q", operation)
	}
}

// youtubeBuildUploadMetadata builds the videos.insert request body's snippet/status.
func youtubeBuildUploadMetadata(title, description string, tags []string, categoryID, privacyStatus string) map[string]interface{} {
	return map[string]interface{}{
		"snippet": map[string]interface{}{
			"title":       title,
			"description": description,
			"tags":        tags,
			"categoryId":  categoryID,
		},
		"status": map[string]interface{}{
			"privacyStatus": privacyStatus,
		},
	}
}

// youtubeUploadVideo performs the two-step resumable upload: an initial
// metadata POST to obtain a session URL (from the Location header), then a
// PUT of the video file bytes to that session URL.
func youtubeUploadVideo(ctx context.Context, accessToken string, metadata map[string]interface{}, videoPath string) (map[string]interface{}, error) {
	metaBody, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshaling metadata: %w", err)
	}

	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, youtubeUploadURL, bytes.NewReader(metaBody))
	if err != nil {
		return nil, err
	}
	initReq.Header.Set("Authorization", "Bearer "+accessToken)
	initReq.Header.Set("Content-Type", "application/json; charset=UTF-8")
	initReq.Header.Set("X-Upload-Content-Type", "video/*")

	initResp, err := httpClient.Do(initReq)
	if err != nil {
		return nil, fmt.Errorf("initiating upload session: %w", err)
	}
	defer initResp.Body.Close()
	if initResp.StatusCode < 200 || initResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(initResp.Body)
		return nil, fmt.Errorf("initiating upload session: HTTP %d: %s", initResp.StatusCode, string(errBody))
	}
	sessionURL := initResp.Header.Get("Location")
	if sessionURL == "" {
		return nil, fmt.Errorf("upload session did not return a Location header")
	}

	f, err := os.Open(videoPath)
	if err != nil {
		return nil, fmt.Errorf("opening video file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat video file: %w", err)
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, f)
	if err != nil {
		return nil, err
	}
	uploadReq.ContentLength = info.Size()
	uploadReq.Header.Set("Content-Type", "video/*")

	uploadResp, err := httpClient.Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("uploading video bytes: %w", err)
	}
	defer uploadResp.Body.Close()
	respBody, err := io.ReadAll(uploadResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading upload response: %w", err)
	}
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return nil, fmt.Errorf("uploading video bytes: HTTP %d: %s", uploadResp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing upload response: %w", err)
	}
	return result, nil
}

// youtubeParseVideoStats extracts view/like/comment counts from a
// videos.list(part=statistics) response.
func youtubeParseVideoStats(raw map[string]interface{}) (map[string]interface{}, error) {
	items, _ := raw["items"].([]interface{})
	if len(items) == 0 {
		return nil, fmt.Errorf("no video found for the given video_id")
	}
	item, _ := items[0].(map[string]interface{})
	stats, _ := item["statistics"].(map[string]interface{})
	return map[string]interface{}{
		"view_count":    stats["viewCount"],
		"like_count":    stats["likeCount"],
		"comment_count": stats["commentCount"],
	}, nil
}

// youtubeParseSearchResults flattens a search.list(type=video) response into
// workflow items (one per video result).
func youtubeParseSearchResults(raw map[string]interface{}) ([]workflow.Item, error) {
	items, _ := raw["items"].([]interface{})
	out := make([]workflow.Item, 0, len(items))
	for _, it := range items {
		result, _ := it.(map[string]interface{})
		id, _ := result["id"].(map[string]interface{})
		snippet, _ := result["snippet"].(map[string]interface{})
		out = append(out, workflow.NewItem(map[string]interface{}{
			"video_id":      id["videoId"],
			"title":         snippet["title"],
			"description":   snippet["description"],
			"channel_title": snippet["channelTitle"],
			"published_at":  snippet["publishedAt"],
		}))
	}
	return out, nil
}

// youtubeParseCommentThreads flattens a commentThreads.list response into
// workflow items (one per top-level comment).
func youtubeParseCommentThreads(raw map[string]interface{}) ([]workflow.Item, error) {
	items, _ := raw["items"].([]interface{})
	out := make([]workflow.Item, 0, len(items))
	for _, it := range items {
		thread, _ := it.(map[string]interface{})
		snippet, _ := thread["snippet"].(map[string]interface{})
		topLevel, _ := snippet["topLevelComment"].(map[string]interface{})
		commentSnippet, _ := topLevel["snippet"].(map[string]interface{})
		out = append(out, workflow.NewItem(map[string]interface{}{
			"comment_id":   topLevel["id"],
			"author":       commentSnippet["authorDisplayName"],
			"text":         commentSnippet["textDisplay"],
			"like_count":   commentSnippet["likeCount"],
			"published_at": commentSnippet["publishedAt"],
		}))
	}
	return out, nil
}
