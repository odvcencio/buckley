package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/fluffy-ui/audio"
	"github.com/odvcencio/fluffy-ui/audio/execdriver"
)

const (
	audioCueMessage      = "buckley.message"
	audioCueToolComplete = "buckley.tool.complete"
	audioCueError        = "buckley.error"
	audioCueStreamStart  = "buckley.stream.start"
)

// AudioConfig controls UI audio behavior.
type AudioConfig struct {
	Enabled      bool
	AssetsPath   string
	MasterVolume int
	SFXVolume    int
	MusicVolume  int
	Muted        bool
}

func buildAudioService(cfg AudioConfig) audio.Service {
	if !cfg.Enabled {
		return audio.Disabled{}
	}
	assets := strings.TrimSpace(cfg.AssetsPath)
	if assets == "" {
		return audio.Disabled{}
	}
	cmd, ok := execdriver.DetectCommand()
	if !ok {
		return audio.Disabled{}
	}
	cues, sources := audioCues(assets)
	if len(cues) == 0 || len(sources) == 0 {
		return audio.Disabled{}
	}
	driver := execdriver.NewDriver(execdriver.Config{Command: cmd, Sources: sources})
	mgr := audio.NewManager(driver, cues...)
	mgr.SetMasterVolume(clampVolume(cfg.MasterVolume, 100))
	mgr.SetSFXVolume(clampVolume(cfg.SFXVolume, 80))
	mgr.SetMusicVolume(clampVolume(cfg.MusicVolume, 60))
	if cfg.Muted {
		mgr.SetMuted(true)
	}
	return mgr
}

func audioCues(assetsDir string) ([]audio.Cue, map[string]execdriver.Source) {
	assetsDir = strings.TrimSpace(assetsDir)
	if assetsDir == "" {
		return nil, nil
	}
	sources := make(map[string]execdriver.Source)
	cues := make([]audio.Cue, 0, 4)

	add := func(id, filename string, cue audio.Cue) {
		path := filepath.Join(assetsDir, filename)
		if _, err := os.Stat(path); err != nil {
			return
		}
		sources[id] = execdriver.Source{Path: path}
		cue.ID = id
		cues = append(cues, cue)
	}

	add(audioCueMessage, "message.wav", audio.Cue{Kind: audio.KindSFX, Volume: 80, Cooldown: 150 * time.Millisecond})
	add(audioCueToolComplete, "tool-complete.wav", audio.Cue{Kind: audio.KindSFX, Volume: 85, Cooldown: 200 * time.Millisecond})
	add(audioCueError, "error.wav", audio.Cue{Kind: audio.KindSFX, Volume: 90, Cooldown: 300 * time.Millisecond})
	add(audioCueStreamStart, "stream-start.wav", audio.Cue{Kind: audio.KindSFX, Volume: 70, Cooldown: 400 * time.Millisecond})

	return cues, sources
}

func clampVolume(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value > 100 {
		return 100
	}
	return value
}
