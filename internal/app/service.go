package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"maxwell/internal/config"
	"maxwell/internal/convert"
	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/queue"
	"maxwell/internal/storage"
	"maxwell/internal/torrent"
	"maxwell/internal/vpn"
)

var ErrUnsafeVPN = errors.New("vpn gate is not safe")

type VPNGate interface {
	Status(ctx context.Context) (model.VPNState, vpn.Signals, error)
}

type Service struct {
	cfg       config.Config
	gate      VPNGate
	torrents  torrent.Client
	uploader  storage.Uploader
	converter convert.Converter
	store     *queue.Store
	bus       *events.Bus
	now       func() time.Time
}

func (s *Service) retryDecision(attempts int) (time.Time, bool) {
	maxAttempts := s.cfg.Workers.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if attempts >= maxAttempts {
		return time.Now().UTC(), false
	}
	base := s.cfg.Workers.BackoffSeconds
	if base <= 0 {
		base = 1
	}
	backoff := time.Duration(base*(1<<(attempts-1))) * time.Second
	return time.Now().UTC().Add(backoff), true
}

type Dependencies struct {
	Config    config.Config
	Gate      VPNGate
	Torrents  torrent.Client
	Uploader  storage.Uploader
	Converter convert.Converter
	Store     *queue.Store
	Bus       *events.Bus
}

func NewService(dep Dependencies) (*Service, error) {
	if dep.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if dep.Torrents == nil {
		return nil, fmt.Errorf("torrent client is required")
	}
	if dep.Uploader == nil {
		return nil, fmt.Errorf("uploader is required")
	}
	if dep.Converter == nil {
		return nil, fmt.Errorf("converter is required")
	}
	if dep.Gate == nil {
		dep.Gate = vpn.NewGate(vpn.StaticDetector{})
	}
	if dep.Bus == nil {
		dep.Bus = events.NewBus()
	}
	return &Service{
		cfg:       dep.Config,
		gate:      dep.Gate,
		torrents:  dep.Torrents,
		uploader:  dep.Uploader,
		converter: dep.Converter,
		store:     dep.Store,
		bus:       dep.Bus,
		now:       time.Now,
	}, nil
}

func (s *Service) Close() error {
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Service) EventBus() *events.Bus { return s.bus }

func (s *Service) VPNStatus(ctx context.Context) (model.VPNState, vpn.Signals, error) {
	return s.gate.Status(ctx)
}

func (s *Service) ListTorrents(ctx context.Context) ([]model.Torrent, error) {
	return s.torrents.List(ctx)
}

func (s *Service) AddMagnet(ctx context.Context, magnet string) (string, error) {
	if strings.TrimSpace(magnet) == "" {
		return "", fmt.Errorf("magnet is required")
	}
	if s.cfg.VPN.RequireSafeForMagnetAdds {
		state, _, err := s.VPNStatus(ctx)
		if err != nil {
			return "", fmt.Errorf("vpn status: %w", err)
		}
		if state != model.VPNStateSafe {
			_ = s.torrents.PauseAll(ctx)
			_ = s.store.AddEvent(ctx, "warn", "vpn_block", "blocked magnet add because vpn state is "+string(state))
			return "", fmt.Errorf("%w: %s", ErrUnsafeVPN, state)
		}
	}
	id, err := s.torrents.AddMagnet(ctx, magnet, s.cfg.Torrent.DownloadDir)
	if err != nil {
		return "", err
	}
	_ = s.store.AddEvent(ctx, "info", "magnet_added", magnet)
	s.bus.Publish(events.Message{Type: "magnet_added", Body: magnet})
	return id, nil
}

func (s *Service) SyncCompletedDownloads(ctx context.Context) error {
	state, _, err := s.VPNStatus(ctx)
	if err != nil {
		return err
	}
	if state != model.VPNStateSafe {
		if err := s.torrents.PauseAll(ctx); err != nil {
			return err
		}
		_ = s.store.AddEvent(ctx, "warn", "vpn_pause", "paused torrents due to vpn state "+string(state))
	}

	list, err := s.torrents.List(ctx)
	if err != nil {
		return err
	}
	for _, t := range list {
		if !t.Completed {
			continue
		}
		source := filepath.Join(t.SavePath, t.Name)
		output := buildOutputPath(s.cfg.Paths.ProcessedDir, t.Name)
		if err := s.store.RecordDownload(ctx, t.ID, source, "completed"); err != nil {
			return err
		}
		inserted, err := s.store.EnqueueConversion(ctx, t.ID, source, output, s.cfg.FFmpeg.Preset)
		if err != nil {
			return err
		}
		if inserted {
			_ = s.store.AddEvent(ctx, "info", "conversion_queued", output)
			s.bus.Publish(events.Message{Type: "conversion_queued", Body: output})
		}
	}
	return nil
}

func buildOutputPath(processedDir, name string) string {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = base
	}
	safe := sanitize(stem)
	return filepath.Join(processedDir, safe+".mp4")
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, " ", "_")
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "")
	s = repl.Replace(s)
	if s == "" {
		return "output"
	}
	return s
}

func (s *Service) ProcessOnce(ctx context.Context) error {
	if err := s.processConversionOnce(ctx); err != nil {
		return err
	}
	if err := s.processUploadOnce(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) processConversionOnce(ctx context.Context) error {
	job, err := s.store.NextQueuedConversion(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	attempts := job.Attempts + 1
	if err := s.store.MarkConversionRunning(ctx, job.ID, attempts); err != nil {
		return err
	}

	if err := s.converter.Convert(ctx, job.InputPath, job.OutputPath, job.Preset); err != nil {
		retryAt, requeue := s.retryDecision(attempts)
		_ = s.store.MarkConversionFailed(ctx, job.ID, err.Error(), retryAt, requeue)
		_ = s.store.AddEvent(ctx, "error", "conversion_failed", err.Error())
		return nil
	}
	if err := s.store.MarkConversionDone(ctx, job.ID); err != nil {
		return err
	}
	key := s.objectKeyForUpload(job.OutputPath)
	if _, err := s.store.EnqueueUpload(ctx, job.OutputPath, key); err != nil {
		return err
	}
	_ = s.store.AddEvent(ctx, "info", "conversion_done", job.OutputPath)
	s.bus.Publish(events.Message{Type: "conversion_done", Body: job.OutputPath})
	return nil
}

func (s *Service) objectKeyForUpload(path string) string {
	file := filepath.Base(path)
	t := s.now().UTC().Format("2006/01/02")
	return t + "/" + sanitize(strings.TrimSuffix(file, filepath.Ext(file))) + filepath.Ext(file)
}

func (s *Service) processUploadOnce(ctx context.Context) error {
	job, err := s.store.NextQueuedUpload(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	attempts := job.Attempts + 1
	if err := s.store.MarkUploadRunning(ctx, job.ID, attempts); err != nil {
		return err
	}
	url, err := s.uploader.Upload(ctx, job.FilePath, job.ObjectKey)
	if err != nil {
		retryAt, requeue := s.retryDecision(attempts)
		_ = s.store.MarkUploadFailed(ctx, job.ID, err.Error(), retryAt, requeue)
		_ = s.store.AddEvent(ctx, "error", "upload_failed", err.Error())
		return nil
	}
	if err := s.store.MarkUploadDone(ctx, job.ID, url); err != nil {
		return err
	}
	_ = s.store.AddEvent(ctx, "info", "upload_done", url)
	s.bus.Publish(events.Message{Type: "upload_done", Body: url})
	return nil
}

func (s *Service) ListConversionJobs(ctx context.Context) ([]model.ConversionJob, error) {
	return s.store.ListConversionJobs(ctx)
}

func (s *Service) ListUploadJobs(ctx context.Context) ([]model.UploadJob, error) {
	return s.store.ListUploadJobs(ctx)
}

func (s *Service) ListLinks(ctx context.Context, limit int) ([]model.LinkRecord, error) {
	return s.store.ListLinks(ctx, limit)
}

func (s *Service) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	return s.store.ListEvents(ctx, limit)
}

func (s *Service) Stats(ctx context.Context) (map[string]int64, error) {
	return s.store.Stats(ctx)
}
