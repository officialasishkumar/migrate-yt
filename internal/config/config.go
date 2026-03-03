package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds runtime options for local and CI runs.
type Config struct {
	SourceChannel      string
	MaxWorkers         int
	UploadPrivacy      string
	PlaylistPrivacy    string
	TempDir            string
	UploadStateFile    string
	LegacyUploadedFile string
	TokenFile          string
	ClientSecretFile   string
	TokenJSON          string
	DeleteTokenOnExit  bool
	NonInteractiveAuth bool
}

func Load() (Config, error) {
	_ = godotenv.Load()

	sourceChannel := strings.TrimSpace(os.Getenv("SOURCE_CHANNEL"))
	if sourceChannel == "" {
		sourceChannel = strings.TrimSpace(os.Getenv("TARGET_CHANNEL"))
	}
	if sourceChannel == "" {
		return Config{}, fmt.Errorf("SOURCE_CHANNEL (or TARGET_CHANNEL) is required")
	}

	maxWorkers := parseIntEnv("MAX_WORKERS", 3)
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	uploadPrivacy := strings.TrimSpace(os.Getenv("UPLOAD_PRIVACY"))
	if uploadPrivacy == "" {
		uploadPrivacy = "private"
	}

	playlistPrivacy := strings.TrimSpace(os.Getenv("PLAYLIST_PRIVACY"))
	if playlistPrivacy == "" {
		playlistPrivacy = uploadPrivacy
	}

	tempDir := strings.TrimSpace(os.Getenv("TEMP_DIR"))
	if tempDir == "" {
		tempDir = "temp"
	}

	uploadStateFile := strings.TrimSpace(os.Getenv("UPLOAD_STATE_FILE"))
	if uploadStateFile == "" {
		uploadStateFile = "uploads_state.json"
	}

	legacyUploadedFile := strings.TrimSpace(os.Getenv("LEGACY_UPLOADED_FILE"))
	if legacyUploadedFile == "" {
		legacyUploadedFile = "uploaded.txt"
	}

	tokenFile := strings.TrimSpace(os.Getenv("TOKEN_FILE"))
	if tokenFile == "" {
		tokenFile = "token.json"
	}

	clientSecretFile := strings.TrimSpace(os.Getenv("CLIENT_SECRET_FILE"))
	if clientSecretFile == "" {
		clientSecretFile = "client_secret.json"
	}

	nonInteractive := parseBoolEnv("NON_INTERACTIVE_AUTH", false)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CI")), "true") {
		nonInteractive = true
	}

	return Config{
		SourceChannel:      sourceChannel,
		MaxWorkers:         maxWorkers,
		UploadPrivacy:      uploadPrivacy,
		PlaylistPrivacy:    playlistPrivacy,
		TempDir:            tempDir,
		UploadStateFile:    uploadStateFile,
		LegacyUploadedFile: legacyUploadedFile,
		TokenFile:          tokenFile,
		ClientSecretFile:   clientSecretFile,
		TokenJSON:          os.Getenv("YT_TOKEN_JSON"),
		DeleteTokenOnExit:  parseBoolEnv("DELETE_TOKEN_ON_EXIT", false),
		NonInteractiveAuth: nonInteractive,
	}, nil
}

func parseIntEnv(key string, defaultValue int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func parseBoolEnv(key string, defaultValue bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}
