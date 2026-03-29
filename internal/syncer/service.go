package syncer

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"ytclone/internal/config"
	"ytclone/internal/model"
	"ytclone/internal/state"
	"ytclone/internal/textutil"
	ytclient "ytclone/internal/youtube"
)

type downloader interface {
	FetchChannelVideos(channelURL string) ([]model.VideoJob, error)
	ResolveChannelID(channelURL string) (string, error)
	DownloadVideo(videoID string) (*model.VideoMeta, error)
}

type youtubeAPI interface {
	BuildSourcePlaylistIndex(ctx context.Context, sourceChannelID string) (map[string][]string, error)
	ListDestinationUploads(ctx context.Context) (ytclient.DestinationInventory, error)
	UploadVideo(ctx context.Context, meta model.VideoMeta, sourceVideoID string, privacy string) (string, error)
	EnsureVideoInPlaylists(ctx context.Context, videoID string, playlistTitles []string, playlistPrivacy string) error
}

// Service orchestrates source scan, dedupe, upload, and playlist mirroring.
type Service struct {
	cfg        config.Config
	downloader downloader
	youtube    youtubeAPI
	store      *state.Store
}

func New(cfg config.Config, downloader downloader, youtube youtubeAPI, store *state.Store) *Service {
	return &Service{cfg: cfg, downloader: downloader, youtube: youtube, store: store}
}

type pendingJob struct {
	video     model.VideoJob
	playlists []string
}

type result struct {
	sourceID string
	title    string
	destID   string
	err      error
}

func (s *Service) Run(ctx context.Context) error {
	log.Printf("Scanning source channel: %s", s.cfg.SourceChannel)
	sourceVideos, err := s.downloader.FetchChannelVideos(s.cfg.SourceChannel)
	if err != nil {
		return err
	}
	log.Printf("Found %d source videos", len(sourceVideos))

	sourceChannelID, err := s.downloader.ResolveChannelID(s.cfg.SourceChannel)
	if err != nil {
		return err
	}
	log.Printf("Resolved source channel id: %s", sourceChannelID)

	sourcePlaylistIndex, err := s.youtube.BuildSourcePlaylistIndex(ctx, sourceChannelID)
	if err != nil {
		return err
	}
	log.Printf("Indexed playlist memberships for %d videos", len(sourcePlaylistIndex))

	destinationInventory, err := s.youtube.ListDestinationUploads(ctx)
	if err != nil {
		return err
	}
	log.Printf("Destination dedupe inventory: %d source-marked, %d by-title", len(destinationInventory.BySourceID), len(destinationInventory.ByTitle))

	stateSnapshot := s.store.Snapshot()
	pending := make([]pendingJob, 0)
	knownTitleToDest := make(map[string]string)
	seenPendingTitles := make(map[string]struct{})

	for _, video := range sourceVideos {
		playlists := sourcePlaylistIndex[video.ID]

		if record, ok := stateSnapshot[video.ID]; ok && record.DestinationVideoID != "" {
			knownTitleToDest[textutil.NormalizeTitle(video.Title)] = record.DestinationVideoID
			if err := s.youtube.EnsureVideoInPlaylists(ctx, record.DestinationVideoID, playlists, s.cfg.PlaylistPrivacy); err != nil {
				log.Printf("warning: could not sync playlist memberships for existing record %s: %v", video.ID, err)
			}
			continue
		}

		if destID, ok := destinationInventory.BySourceID[video.ID]; ok {
			if err := s.upsertRecord(video, destID, playlists); err != nil {
				return err
			}
			knownTitleToDest[textutil.NormalizeTitle(video.Title)] = destID
			if err := s.youtube.EnsureVideoInPlaylists(ctx, destID, playlists, s.cfg.PlaylistPrivacy); err != nil {
				log.Printf("warning: could not sync playlist memberships for source-id matched video %s: %v", video.ID, err)
			}
			continue
		}

		titleKey := textutil.NormalizeTitle(video.Title)
		if titleKey != "" {
			if destID, ok := knownTitleToDest[titleKey]; ok {
				if err := s.upsertRecord(video, destID, playlists); err != nil {
					return err
				}
				if err := s.youtube.EnsureVideoInPlaylists(ctx, destID, playlists, s.cfg.PlaylistPrivacy); err != nil {
					log.Printf("warning: could not sync playlist memberships for title matched video %s: %v", video.ID, err)
				}
				continue
			}
			if destID, ok := destinationInventory.ByTitle[titleKey]; ok {
				if err := s.upsertRecord(video, destID, playlists); err != nil {
					return err
				}
				knownTitleToDest[titleKey] = destID
				if err := s.youtube.EnsureVideoInPlaylists(ctx, destID, playlists, s.cfg.PlaylistPrivacy); err != nil {
					log.Printf("warning: could not sync playlist memberships for title matched video %s: %v", video.ID, err)
				}
				continue
			}
			if _, exists := seenPendingTitles[titleKey]; exists {
				log.Printf("skipping duplicate source title in same run: %s (%s)", video.Title, video.ID)
				continue
			}
			seenPendingTitles[titleKey] = struct{}{}
		}

		pending = append(pending, pendingJob{video: video, playlists: playlists})
	}

	// Flush any buffered state writes from upserts in the dedup pass above.
	if err := s.store.Flush(); err != nil {
		return err
	}

	if len(pending) == 0 {
		log.Printf("No pending uploads. Backup channel is already up to date.")
		return nil
	}

	log.Printf("Pending uploads: %d", len(pending))
	err = s.processPending(ctx, pending)

	// Flush remaining buffered state writes from the upload pass.
	if flushErr := s.store.Flush(); flushErr != nil {
		if err != nil {
			return fmt.Errorf("sync error: %v; state flush error: %w", err, flushErr)
		}
		return flushErr
	}
	return err
}

func (s *Service) processPending(ctx context.Context, pending []pendingJob) error {
	jobs := make(chan pendingJob)
	results := make(chan result, len(pending))

	workerCount := s.cfg.MaxWorkers
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(pending) {
		workerCount = len(pending)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobs {
				res := s.handleJob(ctx, workerID, job)
				results <- res
			}
		}(i + 1)
	}

	go func() {
		for _, job := range pending {
			jobs <- job
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	success := 0
	fail := 0
	for res := range results {
		if res.err != nil {
			fail++
			log.Printf("failed: %s (%s): %v", res.title, res.sourceID, res.err)
			continue
		}
		success++
		log.Printf("uploaded: %s (%s) -> %s", res.title, res.sourceID, res.destID)
	}

	if fail > 0 {
		return fmt.Errorf("sync finished with %d failures and %d successes", fail, success)
	}
	log.Printf("Sync finished successfully: %d videos uploaded", success)
	return nil
}

func (s *Service) handleJob(ctx context.Context, workerID int, job pendingJob) result {
	log.Printf("worker %d downloading %s (%s)", workerID, job.video.Title, job.video.ID)
	meta, err := s.downloader.DownloadVideo(job.video.ID)
	if err != nil {
		return result{sourceID: job.video.ID, title: job.video.Title, err: err}
	}

	destID, err := s.youtube.UploadVideo(ctx, *meta, job.video.ID, s.cfg.UploadPrivacy)
	if err != nil {
		_ = os.Remove(meta.Filename)
		return result{sourceID: job.video.ID, title: job.video.Title, err: err}
	}

	if err := s.youtube.EnsureVideoInPlaylists(ctx, destID, job.playlists, s.cfg.PlaylistPrivacy); err != nil {
		_ = os.Remove(meta.Filename)
		return result{sourceID: job.video.ID, title: job.video.Title, destID: destID, err: err}
	}

	if err := s.upsertRecord(job.video, destID, job.playlists); err != nil {
		_ = os.Remove(meta.Filename)
		return result{sourceID: job.video.ID, title: job.video.Title, destID: destID, err: err}
	}

	if err := os.Remove(meta.Filename); err != nil {
		log.Printf("warning: worker %d could not remove temp file %s: %v", workerID, meta.Filename, err)
	}

	return result{sourceID: job.video.ID, title: job.video.Title, destID: destID, err: nil}
}

func (s *Service) upsertRecord(video model.VideoJob, destinationVideoID string, playlists []string) error {
	record := model.UploadRecord{
		SourceVideoID:      video.ID,
		SourceTitle:        video.Title,
		DestinationVideoID: destinationVideoID,
		Playlists:          playlists,
		UploadedAt:         time.Now().UTC(),
	}
	return s.store.Upsert(record)
}
