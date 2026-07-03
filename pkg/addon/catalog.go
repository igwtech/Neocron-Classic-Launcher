package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CatalogEntry is one row of the curated addon catalog the launcher shows for
// one-click installs. It is a thin pointer at a GitHub repo (RepoURL) that the
// existing InstallFromRepo path consumes — the catalog adds discovery, not a
// second install mechanism. Users can still paste an arbitrary repo URL.
type CatalogEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Category    string   `json:"category"`
	RepoURL     string   `json:"repoUrl"`
	Version     string   `json:"version"`
	Icon        string   `json:"icon"`
	Requires    []string `json:"requires"`
}

// FetchCatalog GETs the curated catalog JSON. It accepts either a bare array
// ([...]) or an {"addons":[...]} envelope so the hosted file can grow metadata
// around the list without breaking the client. A missing/empty catalog is not
// an error to the caller's UI — it just yields an empty list.
func FetchCatalog(ctx context.Context, url, userAgent string) ([]CatalogEntry, error) {
	if url == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // catalog not published yet — empty, not fatal
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("catalog: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var arr []CatalogEntry
	if json.Unmarshal(data, &arr) == nil {
		return arr, nil
	}
	var env struct {
		Addons []CatalogEntry `json:"addons"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("catalog: bad JSON: %w", err)
	}
	return env.Addons, nil
}
