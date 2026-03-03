package app

import (
	"context"
	"fmt"
	"os"

	"ytclone/internal/config"
	"ytclone/internal/state"
	"ytclone/internal/syncer"
	youtubeclient "ytclone/internal/youtube"
	"ytclone/internal/ytdlp"
)

func Run(ctx context.Context, cfg config.Config) error {
	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}

	store, err := state.New(cfg.UploadStateFile, cfg.LegacyUploadedFile)
	if err != nil {
		return err
	}

	yt, err := youtubeclient.New(ctx, cfg)
	if err != nil {
		return err
	}

	dl := ytdlp.New(cfg.TempDir)
	svc := syncer.New(cfg, dl, yt, store)

	err = svc.Run(ctx)
	if cfg.DeleteTokenOnExit {
		if removeErr := os.Remove(cfg.TokenFile); removeErr != nil && !os.IsNotExist(removeErr) {
			if err != nil {
				return fmt.Errorf("sync error: %v; token cleanup error: %w", err, removeErr)
			}
			return fmt.Errorf("token cleanup error: %w", removeErr)
		}
	}
	return err
}
