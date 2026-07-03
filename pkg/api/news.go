package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// NewsPost is one entry from the launcher news feed. Field names match what the
// web UI's nclSetNews/renderNews expects (title, body, createdAt).
type NewsPost struct {
	ID        json.Number `json:"id"` // server sends a number; tolerate string too
	Title     string      `json:"title"`
	Body      string      `json:"body"` // HTML
	CreatedAt string      `json:"createdAt"`
}

// FetchNews GETs the launcher news feed the official launcher uses:
//
//	GET {apiBase}/news?limit={limit}
//
// This is a global endpoint on the content API (areamc5.neocron.org) — no
// channel, no key, no auth. Returns a newest-first list of posts. limit<=0
// defaults to 10.
func FetchNews(ctx context.Context, apiBase string, limit int, ua string) ([]NewsPost, error) {
	if limit <= 0 {
		limit = 10
	}
	url := fmt.Sprintf("%s/news?limit=%d", apiBase, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("news %s: HTTP %d", url, resp.StatusCode)
	}
	var posts []NewsPost
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("news: bad JSON: %w", err)
	}
	return posts, nil
}
