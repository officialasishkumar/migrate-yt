package model

import "time"

// VideoJob represents a source video discovered from the source channel.
type VideoJob struct {
	ID    string
	Title string
}

// VideoMeta contains metadata and local file path for a downloaded video.
type VideoMeta struct {
	ID          string
	Title       string
	Description string
	Filename    string
}

// UploadRecord is persisted in state so repeated runs are idempotent.
type UploadRecord struct {
	SourceVideoID      string    `json:"source_video_id"`
	SourceTitle        string    `json:"source_title"`
	DestinationVideoID string    `json:"destination_video_id"`
	Playlists          []string  `json:"playlists,omitempty"`
	UploadedAt         time.Time `json:"uploaded_at"`
}
