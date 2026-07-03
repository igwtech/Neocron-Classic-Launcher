// Package api wraps the public neocron.org launcher content API.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Banner is one promo image in a banner group.
type Banner struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Image       string `json:"image"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Order       int    `json:"order"`
	BannerGroup int    `json:"banner_group"`
}

// BannerGroup is a "hero" or "regular" slot holding one or more banners.
type BannerGroup struct {
	ID      int      `json:"id"`
	Order   int      `json:"order"`
	Size    string   `json:"size"`
	Banners []Banner `json:"banners"`
}

// LauncherContent is the GET /api/launcher response.
type LauncherContent struct {
	Success      string        `json:"success"`
	BannerGroups []BannerGroup `json:"banner_groups"`
}

// Client fetches launcher content from neocron.org.
type Client struct {
	url  string
	ua   string
	http *http.Client
}

// New returns a content client for the given endpoint (e.g.
// https://neocron.org/api/launcher).
func New(url, ua string) *Client {
	return &Client{url: url, ua: ua, http: &http.Client{Timeout: 15 * time.Second}}
}

// Banners fetches the current banner groups.
func (c *Client) Banners(ctx context.Context) (*LauncherContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out LauncherContent
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
