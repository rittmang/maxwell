package convert

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"maxwell/internal/config"
)

type Converter interface {
	Name() string
	Convert(ctx context.Context, inputPath, outputPath, preset string) error
}

func New(cfg config.FFmpegConfig) Converter {
	if strings.EqualFold(cfg.Bin, "copy") || cfg.Bin == "" {
		return CopyConverter{}
	}
	return FFmpegConverter{Bin: cfg.Bin, FFProbeBin: cfg.FFProbeBin}
}

type CopyConverter struct{}

func (CopyConverter) Name() string { return "copy" }

func (CopyConverter) Convert(_ context.Context, inputPath, outputPath, _ string) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type FFmpegConverter struct {
	Bin        string
	FFProbeBin string
}

func (c FFmpegConverter) Name() string { return "ffmpeg" }

func (c FFmpegConverter) Convert(ctx context.Context, inputPath, outputPath, preset string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(c.FFProbeBin) != "" {
		probe := exec.CommandContext(ctx, c.FFProbeBin, "-v", "error", "-show_format", "-show_streams", inputPath)
		probeOut, err := probe.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffprobe failed: %w: %s", err, strings.TrimSpace(string(probeOut)))
		}
	}
	args := []string{"-y", "-i", inputPath, "-preset", "fast"}
	switch preset {
	case "h265_1080p_balanced":
		args = append(args, "-c:v", "libx265")
	default:
		args = append(args, "-c:v", "libx264")
	}
	args = append(args, outputPath)
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
