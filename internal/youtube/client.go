package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	ytv3 "google.golang.org/api/youtube/v3"

	"ytclone/internal/config"
	"ytclone/internal/model"
	"ytclone/internal/textutil"
)

const sourceMarkerPrefix = "Backup Source ID:"

var sourceMarkerPattern = regexp.MustCompile(`(?m)^Backup Source ID:\s*([A-Za-z0-9_-]+)\s*$`)

// DestinationInventory is used for dedupe checks against destination channel uploads.
type DestinationInventory struct {
	BySourceID map[string]string
	ByTitle    map[string]string
}

// Client wraps YouTube Data API operations used by the sync service.
type Client struct {
	service *ytv3.Service

	mu                sync.Mutex
	uploadsPlaylistID string
	ownPlaylists      map[string]string
	playlistsLoaded   bool
	playlistVideoSet  map[string]map[string]struct{}
}

func New(ctx context.Context, cfg config.Config) (*Client, error) {
	service, err := newService(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &Client{
		service:          service,
		ownPlaylists:     make(map[string]string),
		playlistVideoSet: make(map[string]map[string]struct{}),
	}, nil
}

func newService(ctx context.Context, cfg config.Config) (*ytv3.Service, error) {
	credentials, err := os.ReadFile(cfg.ClientSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read client secret file (%s): %w", cfg.ClientSecretFile, err)
	}

	oauthConfig, err := google.ConfigFromJSON(credentials, ytv3.YoutubeScope)
	if err != nil {
		return nil, fmt.Errorf("parse oauth config: %w", err)
	}

	tok, err := getToken(ctx, oauthConfig, cfg)
	if err != nil {
		return nil, err
	}

	httpClient := oauthConfig.Client(ctx, tok)
	service, err := ytv3.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create youtube service: %w", err)
	}
	return service, nil
}

func getToken(ctx context.Context, oauthConfig *oauth2.Config, cfg config.Config) (*oauth2.Token, error) {
	tok, err := tokenFromFile(cfg.TokenFile)
	if err == nil {
		return tok, nil
	}

	if strings.TrimSpace(cfg.TokenJSON) != "" {
		tok = &oauth2.Token{}
		if unmarshalErr := json.Unmarshal([]byte(cfg.TokenJSON), tok); unmarshalErr != nil {
			return nil, fmt.Errorf("parse YT_TOKEN_JSON: %w", unmarshalErr)
		}
		if saveErr := saveToken(cfg.TokenFile, tok); saveErr != nil {
			return nil, saveErr
		}
		return tok, nil
	}

	if cfg.NonInteractiveAuth {
		return nil, fmt.Errorf("token file (%s) not found and YT_TOKEN_JSON is empty in non-interactive mode", cfg.TokenFile)
	}

	tok, err = getTokenFromWeb(ctx, oauthConfig)
	if err != nil {
		return nil, err
	}
	if err := saveToken(cfg.TokenFile, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func tokenFromFile(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tok := &oauth2.Token{}
	if err := json.NewDecoder(f).Decode(tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func saveToken(path string, token *oauth2.Token) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create token directory: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open token file: %w", err)
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(token); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

func getTokenFromWeb(ctx context.Context, oauthConfig *oauth2.Config) (*oauth2.Token, error) {
	authURL := oauthConfig.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Open this URL and authorize access:\n\n%s\n\n", authURL)
	fmt.Printf("Paste the authorization code: ")

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("read authorization code: %w", err)
	}

	tok, err := oauthConfig.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("exchange auth code for token: %w", err)
	}
	return tok, nil
}

// BuildSourcePlaylistIndex maps source video IDs to source playlist titles.
func (c *Client) BuildSourcePlaylistIndex(ctx context.Context, sourceChannelID string) (map[string][]string, error) {
	videoPlaylists := make(map[string]map[string]struct{})
	pageToken := ""

	for {
		playlistCall := c.service.Playlists.List([]string{"snippet"}).ChannelId(sourceChannelID).MaxResults(50)
		if pageToken != "" {
			playlistCall = playlistCall.PageToken(pageToken)
		}
		playlistResp, err := playlistCall.Do()
		if err != nil {
			return nil, fmt.Errorf("list source playlists: %w", err)
		}

		for _, playlist := range playlistResp.Items {
			playlistID := strings.TrimSpace(playlist.Id)
			playlistTitle := strings.TrimSpace(playlist.Snippet.Title)
			if playlistID == "" || playlistTitle == "" {
				continue
			}

			itemToken := ""
			for {
				itemsCall := c.service.PlaylistItems.List([]string{"contentDetails"}).PlaylistId(playlistID).MaxResults(50)
				if itemToken != "" {
					itemsCall = itemsCall.PageToken(itemToken)
				}
				itemsResp, itemErr := itemsCall.Do()
				if itemErr != nil {
					return nil, fmt.Errorf("list items for source playlist %s (%s): %w", playlistTitle, playlistID, itemErr)
				}

				for _, item := range itemsResp.Items {
					videoID := strings.TrimSpace(item.ContentDetails.VideoId)
					if videoID == "" {
						continue
					}
					if _, ok := videoPlaylists[videoID]; !ok {
						videoPlaylists[videoID] = make(map[string]struct{})
					}
					videoPlaylists[videoID][playlistTitle] = struct{}{}
				}

				itemToken = itemsResp.NextPageToken
				if itemToken == "" {
					break
				}
			}
		}

		pageToken = playlistResp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	out := make(map[string][]string, len(videoPlaylists))
	for videoID, titles := range videoPlaylists {
		names := make([]string, 0, len(titles))
		for name := range titles {
			names = append(names, name)
		}
		sort.Strings(names)
		out[videoID] = names
	}
	return out, nil
}

// ListDestinationUploads scans destination uploads for source-ID and title dedupe.
func (c *Client) ListDestinationUploads(ctx context.Context) (DestinationInventory, error) {
	uploadsPlaylistID, err := c.getUploadsPlaylistID(ctx)
	if err != nil {
		return DestinationInventory{}, err
	}

	byTitle := make(map[string]string)
	videoIDs := make([]string, 0)
	pageToken := ""

	for {
		call := c.service.PlaylistItems.List([]string{"snippet", "contentDetails"}).PlaylistId(uploadsPlaylistID).MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return DestinationInventory{}, fmt.Errorf("list destination uploads playlist items: %w", err)
		}

		for _, item := range resp.Items {
			videoID := strings.TrimSpace(item.ContentDetails.VideoId)
			if videoID == "" {
				continue
			}
			videoIDs = append(videoIDs, videoID)

			titleKey := textutil.NormalizeTitle(item.Snippet.Title)
			if titleKey != "" {
				if _, exists := byTitle[titleKey]; !exists {
					byTitle[titleKey] = videoID
				}
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	bySourceID := make(map[string]string)
	for i := 0; i < len(videoIDs); i += 50 {
		end := i + 50
		if end > len(videoIDs) {
			end = len(videoIDs)
		}
		chunk := videoIDs[i:end]
		call := c.service.Videos.List([]string{"snippet"}).Id(chunk...)
		resp, err := call.Do()
		if err != nil {
			return DestinationInventory{}, fmt.Errorf("list destination videos for source-marker parsing: %w", err)
		}
		for _, v := range resp.Items {
			sourceID, ok := extractSourceVideoID(v.Snippet.Description)
			if !ok {
				continue
			}
			if _, exists := bySourceID[sourceID]; !exists {
				bySourceID[sourceID] = v.Id
			}
		}
	}

	return DestinationInventory{BySourceID: bySourceID, ByTitle: byTitle}, nil
}

func (c *Client) UploadVideo(ctx context.Context, meta model.VideoMeta, sourceVideoID string, privacy string) (string, error) {
	f, err := os.Open(meta.Filename)
	if err != nil {
		return "", fmt.Errorf("open video file %s: %w", meta.Filename, err)
	}
	defer f.Close()

	description := appendSourceMarker(meta.Description, sourceVideoID)
	upload := &ytv3.Video{
		Snippet: &ytv3.VideoSnippet{
			Title:       meta.Title,
			Description: description,
			CategoryId:  "22",
		},
		Status: &ytv3.VideoStatus{PrivacyStatus: privacy},
	}

	call := c.service.Videos.Insert([]string{"snippet", "status"}, upload)
	resp, err := call.Media(f).Do()
	if err != nil {
		return "", fmt.Errorf("upload video %s: %w", meta.Title, err)
	}
	return resp.Id, nil
}

func (c *Client) EnsureVideoInPlaylists(ctx context.Context, videoID string, playlistTitles []string, playlistPrivacy string) error {
	titles := uniqueNonEmpty(playlistTitles)
	if len(titles) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.loadOwnPlaylistsLocked(ctx); err != nil {
		return err
	}

	for _, title := range titles {
		playlistID, err := c.ensurePlaylistLocked(ctx, title, playlistPrivacy)
		if err != nil {
			return err
		}
		if err := c.ensurePlaylistVideoSetLocked(ctx, playlistID); err != nil {
			return err
		}
		if _, exists := c.playlistVideoSet[playlistID][videoID]; exists {
			continue
		}

		item := &ytv3.PlaylistItem{
			Snippet: &ytv3.PlaylistItemSnippet{
				PlaylistId: playlistID,
				ResourceId: &ytv3.ResourceId{
					Kind:    "youtube#video",
					VideoId: videoID,
				},
			},
		}
		if _, err := c.service.PlaylistItems.Insert([]string{"snippet"}, item).Do(); err != nil {
			return fmt.Errorf("add video %s to playlist %s: %w", videoID, title, err)
		}
		c.playlistVideoSet[playlistID][videoID] = struct{}{}
	}

	return nil
}

func (c *Client) getUploadsPlaylistID(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.uploadsPlaylistID != "" {
		id := c.uploadsPlaylistID
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	resp, err := c.service.Channels.List([]string{"contentDetails"}).Mine(true).Do()
	if err != nil {
		return "", fmt.Errorf("fetch destination channel content details: %w", err)
	}
	if len(resp.Items) == 0 || resp.Items[0].ContentDetails == nil {
		return "", fmt.Errorf("authenticated account has no channel content details")
	}

	uploads := strings.TrimSpace(resp.Items[0].ContentDetails.RelatedPlaylists.Uploads)
	if uploads == "" {
		return "", fmt.Errorf("uploads playlist id not found for destination account")
	}

	c.mu.Lock()
	c.uploadsPlaylistID = uploads
	c.mu.Unlock()
	return uploads, nil
}

func (c *Client) loadOwnPlaylistsLocked(ctx context.Context) error {
	if c.playlistsLoaded {
		return nil
	}

	pageToken := ""
	for {
		call := c.service.Playlists.List([]string{"snippet"}).Mine(true).MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("list destination playlists: %w", err)
		}
		for _, p := range resp.Items {
			title := strings.TrimSpace(p.Snippet.Title)
			if title == "" || p.Id == "" {
				continue
			}
			if _, exists := c.ownPlaylists[title]; !exists {
				c.ownPlaylists[title] = p.Id
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	c.playlistsLoaded = true
	return nil
}

func (c *Client) ensurePlaylistLocked(ctx context.Context, title string, privacy string) (string, error) {
	if playlistID, exists := c.ownPlaylists[title]; exists {
		return playlistID, nil
	}

	playlist := &ytv3.Playlist{
		Snippet: &ytv3.PlaylistSnippet{Title: title, Description: "Mirrored by ytclone backup sync"},
		Status:  &ytv3.PlaylistStatus{PrivacyStatus: privacy},
	}
	resp, err := c.service.Playlists.Insert([]string{"snippet", "status"}, playlist).Do()
	if err != nil {
		return "", fmt.Errorf("create destination playlist %s: %w", title, err)
	}
	c.ownPlaylists[title] = resp.Id
	return resp.Id, nil
}

func (c *Client) ensurePlaylistVideoSetLocked(ctx context.Context, playlistID string) error {
	if _, exists := c.playlistVideoSet[playlistID]; exists {
		return nil
	}

	set := make(map[string]struct{})
	pageToken := ""
	for {
		call := c.service.PlaylistItems.List([]string{"contentDetails"}).PlaylistId(playlistID).MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("list destination playlist items for %s: %w", playlistID, err)
		}
		for _, item := range resp.Items {
			videoID := strings.TrimSpace(item.ContentDetails.VideoId)
			if videoID != "" {
				set[videoID] = struct{}{}
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	c.playlistVideoSet[playlistID] = set
	return nil
}

func appendSourceMarker(description string, sourceVideoID string) string {
	description = strings.TrimSpace(description)
	if sourceVideoID == "" {
		return description
	}
	if _, found := extractSourceVideoID(description); found {
		return description
	}
	if description == "" {
		return fmt.Sprintf("%s %s", sourceMarkerPrefix, sourceVideoID)
	}
	return fmt.Sprintf("%s\n\n%s %s", description, sourceMarkerPrefix, sourceVideoID)
}

func extractSourceVideoID(description string) (string, bool) {
	matches := sourceMarkerPattern.FindStringSubmatch(description)
	if len(matches) != 2 {
		return "", false
	}
	return strings.TrimSpace(matches[1]), true
}

func uniqueNonEmpty(values []string) []string {
	set := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		value := strings.TrimSpace(v)
		if value == "" {
			continue
		}
		if _, exists := set[value]; exists {
			continue
		}
		set[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
