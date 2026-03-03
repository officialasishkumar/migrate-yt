package ytdlp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ytclone/internal/model"
)

type ytdlpInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Client wraps yt-dlp shell calls.
type Client struct {
	tempDir string
}

func New(tempDir string) *Client {
	return &Client{tempDir: tempDir}
}

func (c *Client) FetchChannelVideos(channelURL string) ([]model.VideoJob, error) {
	cmd := exec.Command("yt-dlp", "--flat-playlist", "--print", "%(id)s|%(title)s", channelURL)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("fetch channel videos failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	jobs := make([]model.VideoJob, 0)
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		jobs = append(jobs, model.VideoJob{ID: strings.TrimSpace(parts[0]), Title: strings.TrimSpace(parts[1])})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan yt-dlp output: %w", err)
	}

	return jobs, nil
}

func (c *Client) ResolveChannelID(channelURL string) (string, error) {
	cmd := exec.Command("yt-dlp", "--skip-download", "--playlist-items", "1", "--print", "%(channel_id)s", channelURL)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("resolve source channel id failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		id := strings.TrimSpace(scanner.Text())
		if id != "" && id != "NA" {
			return id, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan channel id: %w", err)
	}

	return "", fmt.Errorf("yt-dlp did not return source channel id")
}

func (c *Client) DownloadVideo(videoID string) (*model.VideoMeta, error) {
	if err := os.MkdirAll(c.tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}

	videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
	outputTemplate := filepath.Join(c.tempDir, fmt.Sprintf("%s.%%(ext)s", videoID))

	cmd := exec.Command("yt-dlp",
		"--dump-json",
		"--no-simulate",
		"--merge-output-format", "mp4",
		"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
		"-o", outputTemplate,
		videoURL,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("yt-dlp download failed for %s: %w (%s)", videoID, err, strings.TrimSpace(stderr.String()))
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("yt-dlp did not emit metadata for %s", videoID)
	}

	var info ytdlpInfo
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &info); err != nil {
		return nil, fmt.Errorf("parse yt-dlp metadata for %s: %w", videoID, err)
	}

	actualFile, err := c.findDownloadedFile(videoID)
	if err != nil {
		return nil, fmt.Errorf("resolve downloaded file for %s: %w (%s)", videoID, err, strings.TrimSpace(stderr.String()))
	}

	return &model.VideoMeta{
		ID:          info.ID,
		Title:       info.Title,
		Description: info.Description,
		Filename:    actualFile,
	}, nil
}

func (c *Client) findDownloadedFile(videoID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(c.tempDir, videoID+".*"))
	if err != nil {
		return "", fmt.Errorf("glob temp files: %w", err)
	}

	var candidate string
	for _, match := range matches {
		if strings.HasSuffix(match, ".part") || strings.HasSuffix(match, ".ytdl") {
			continue
		}
		candidate = match
		suffix := filepath.Base(match)[len(videoID):]
		if !strings.Contains(suffix, ".f") {
			return match, nil
		}
	}

	if candidate == "" {
		return "", fmt.Errorf("no merged media file found")
	}
	return candidate, nil
}
