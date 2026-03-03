# YouTube Backup Migrator

A Go application that mirrors a source YouTube channel into your backup channel with idempotent reruns.

## What It Does

- Scans all source uploads from a source channel URL.
- Mirrors source playlist memberships:
  - discovers playlists from the source channel,
  - creates missing playlists in your destination channel,
  - adds each uploaded/matched video to the equivalent destination playlists.
- Prevents duplicate uploads on reruns using three layers:
  - persisted state file (`uploads_state.json`),
  - source-ID marker embedded in uploaded video descriptions,
  - destination title matching fallback.
- Supports scheduled CI/CD backups (weekly cron workflow included).

## Architecture (LLD)

Business logic is moved outside `main` into focused packages:

- `internal/config`: env parsing and runtime config.
- `internal/ytdlp`: source scanning and media download.
- `internal/youtube`: YouTube Data API client, playlist sync, destination inventory.
- `internal/state`: persisted upload registry.
- `internal/syncer`: orchestration flow (scan -> dedupe -> upload -> playlist attach).
- `internal/app`: composition root used by `main.go`.

`main.go` is intentionally thin.

## Prerequisites

- Go 1.25+
- `yt-dlp`
- `ffmpeg`
- Google OAuth credentials (`client_secret.json`)

Enable YouTube Data API v3 and create an OAuth Desktop App client in Google Cloud.

## Configuration

Create `.env`:

```env
# Source channel to back up (URL or handle URL)
SOURCE_CHANNEL="https://www.youtube.com/@YourSourceChannel"

# Upload behavior
UPLOAD_PRIVACY="private"
PLAYLIST_PRIVACY="private"
MAX_WORKERS=3

# Paths
TEMP_DIR="temp"
UPLOAD_STATE_FILE="uploads_state.json"
LEGACY_UPLOADED_FILE="uploaded.txt"
CLIENT_SECRET_FILE="client_secret.json"
TOKEN_FILE="token.json"

# Auth behavior
NON_INTERACTIVE_AUTH=false
DELETE_TOKEN_ON_EXIT=false

# Optional: raw JSON token for CI/non-interactive runs
# YT_TOKEN_JSON='{"access_token":"...","refresh_token":"..."}'
```

Backward compatibility:

- If `uploads_state.json` is missing and `uploaded.txt` exists, legacy IDs are auto-imported.

## Run Locally

```bash
go run .
```

First run (interactive):

- Script prints OAuth URL.
- Authorize with destination channel account.
- Paste auth code in terminal.

## Docker

```bash
docker compose up -d
```

Mounted files:

- `.env`
- `client_secret.json`
- `uploads_state.json`
- `uploaded.txt` (legacy import path)
- `temp/`

## CI/CD Weekly Backup

Workflow file: `.github/workflows/weekly-backup.yml`

Runs:

- every 7 days via cron
- manually via `workflow_dispatch`

Required GitHub Secrets:

- `YT_CLIENT_SECRET_JSON` (full JSON content)
- `YT_TOKEN_JSON` (full token JSON, recommended for non-interactive runs)
- `SOURCE_CHANNEL`

State persistence between workflow runs:

- `uploads_state.json` is cached with GitHub Actions cache.

## Notes

- Dedupe by title is a fallback. Source-ID marker and state file are primary.
- Playlists are matched by playlist title from source -> destination.
- Private source playlists are not accessible unless authorized accordingly.
