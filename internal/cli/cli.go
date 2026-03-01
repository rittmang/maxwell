package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"maxwell/internal/app"
	"maxwell/internal/config"
	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/queue"
	"maxwell/internal/torrent"
	"maxwell/internal/vpn"
	webui "maxwell/internal/web"
)

type ServiceAPI interface {
	Close() error
	VPNStatus(context.Context) (model.VPNState, vpn.Signals, error)
	ListTorrents(context.Context) ([]model.Torrent, error)
	AddMagnet(context.Context, string) (string, error)
	PauseTorrent(context.Context, string) error
	ResumeTorrent(context.Context, string) error
	SyncCompletedDownloads(context.Context) error
	ProcessOnce(context.Context) error
	ListConversionJobs(context.Context) ([]model.ConversionJob, error)
	ListUploadJobs(context.Context) ([]model.UploadJob, error)
	PauseConversionJob(context.Context, int64) error
	ResumeConversionJob(context.Context, int64) error
	PauseUploadJob(context.Context, int64) error
	ResumeUploadJob(context.Context, int64) error
	ListLinks(context.Context, int) ([]model.LinkRecord, error)
	ListEvents(context.Context, int) ([]model.Event, error)
	Stats(context.Context) (map[string]int64, error)
	EventBus() *events.Bus
}

type Builder func(configPath string) (ServiceAPI, error)

type Runner struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Build   Builder
	Timeout time.Duration
}

func NewRunner(stdout, stderr io.Writer) *Runner {
	return &Runner{
		Stdout:  stdout,
		Stderr:  stderr,
		Build:   defaultBuilder,
		Timeout: 10 * time.Second,
	}
}

func defaultBuilder(configPath string) (ServiceAPI, error) {
	tryAutoSetupQBit(configPath)
	svc, _, err := app.Build(configPath)
	return svc, err
}

func (r *Runner) Execute(args []string) int {
	if r.Stdout == nil || r.Stderr == nil {
		return 1
	}
	if len(args) == 0 {
		r.printUsage()
		return 0
	}

	configPath, rest := parseGlobalConfig(args)
	if len(rest) == 0 {
		r.printUsage()
		return 0
	}

	sub := rest[0]
	subArgs := rest[1:]

	switch sub {
	case "doctor":
		return r.cmdDoctor(configPath)
	case "run":
		return r.cmdRun(configPath, subArgs)
	case "vpn":
		return r.cmdVPN(configPath, subArgs)
	case "torrents":
		return r.cmdTorrents(configPath, subArgs)
	case "queue":
		return r.cmdQueue(configPath, subArgs)
	case "links":
		return r.cmdLinks(configPath, subArgs)
	case "web":
		return r.cmdWeb(configPath, subArgs)
	default:
		fmt.Fprintf(r.Stderr, "unknown command: %s\n", sub)
		r.printUsage()
		return 2
	}
}

func parseGlobalConfig(args []string) (string, []string) {
	if len(args) >= 2 && strings.HasPrefix(args[0], "--config") {
		if args[0] == "--config" {
			return args[1], args[2:]
		}
		if strings.HasPrefix(args[0], "--config=") {
			return strings.TrimPrefix(args[0], "--config="), args[1:]
		}
	}
	return "", args
}

func (r *Runner) cmdDoctor(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "doctor failed: %v\n", err)
		return 1
	}
	cfg.Torrent.DownloadDir = config.ResolvePath(configPath, cfg.Torrent.DownloadDir)
	cfg.Paths.DownloadsDir = config.ResolvePath(configPath, cfg.Paths.DownloadsDir)
	cfg.Paths.ProcessedDir = config.ResolvePath(configPath, cfg.Paths.ProcessedDir)
	ss := cfg.EffectiveStateStore()
	if strings.EqualFold(ss.Driver, "sqlite") {
		ss.DSN = config.ResolvePath(configPath, ss.DSN)
	}

	type check struct {
		name string
		err  error
	}
	var checks []check

	checks = append(checks, check{name: "download_dir_writable", err: ensureDirWritable(cfg.Torrent.DownloadDir)})
	checks = append(checks, check{name: "processed_dir_writable", err: ensureDirWritable(cfg.Paths.ProcessedDir)})
	checks = append(checks, check{name: "state_store_connectivity", err: checkStore(ss)})
	checks = append(checks, check{name: "ffmpeg_bin", err: checkBinary(cfg.FFmpeg.Bin, true)})
	checks = append(checks, check{name: "ffprobe_bin", err: checkBinary(cfg.FFmpeg.FFProbeBin, false)})
	checks = append(checks, check{name: "torrent_api", err: checkTorrentAPI(cfg.Torrent)})

	failed := false
	for _, c := range checks {
		if c.err != nil {
			failed = true
			fmt.Fprintf(r.Stdout, "%s=fail reason=%v\n", c.name, c.err)
			continue
		}
		fmt.Fprintf(r.Stdout, "%s=ok\n", c.name)
	}
	fmt.Fprintf(r.Stdout, "torrent_provider=%s\n", cfg.Torrent.Provider)
	fmt.Fprintf(r.Stdout, "storage_provider=%s\n", cfg.Storage.Provider)
	fmt.Fprintf(r.Stdout, "state_store_driver=%s\n", ss.Driver)
	fmt.Fprintf(r.Stdout, "web_bind=%s\n", cfg.Security.WebBind)
	if failed {
		fmt.Fprintf(r.Stderr, "doctor: failed\n")
		return 1
	}
	fmt.Fprintf(r.Stdout, "doctor: ok\n")
	return 0
}

func (r *Runner) cmdRun(configPath string, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	cycles := fs.Int("cycles", 1, "number of sync/process cycles")
	forever := fs.Bool("forever", false, "run continuously until interrupted")
	interval := fs.Duration("interval", 8*time.Second, "poll/process interval in forever mode")
	requireSafeVPN := fs.Bool("require-safe-vpn", true, "fail startup if vpn is not SAFE")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	if *requireSafeVPN {
		ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
		state, _, err := svc.VPNStatus(ctx)
		cancel()
		if err != nil {
			fmt.Fprintf(r.Stderr, "vpn startup check: %v\n", err)
			return 1
		}
		if state != model.VPNStateSafe {
			fmt.Fprintf(r.Stderr, "vpn startup check failed: state=%s\n", state)
			return 1
		}
	}

	if *forever {
		if *interval <= 0 {
			fmt.Fprintln(r.Stderr, "interval must be > 0")
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		fmt.Fprintf(r.Stdout, "run loop started interval=%s\n", interval.String())
		for {
			if err := svc.SyncCompletedDownloads(ctx); err != nil {
				fmt.Fprintf(r.Stderr, "sync downloads: %v\n", err)
			}
			if err := svc.ProcessOnce(ctx); err != nil {
				fmt.Fprintf(r.Stderr, "process jobs: %v\n", err)
			}
			select {
			case <-ctx.Done():
				fmt.Fprintln(r.Stdout, "run loop stopped")
				return 0
			case <-ticker.C:
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()

	for i := 0; i < *cycles; i++ {
		if err := svc.SyncCompletedDownloads(ctx); err != nil {
			fmt.Fprintf(r.Stderr, "sync downloads: %v\n", err)
			return 1
		}
		if err := svc.ProcessOnce(ctx); err != nil {
			fmt.Fprintf(r.Stderr, "process jobs: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(r.Stdout, "run complete (%d cycles)\n", *cycles)
	return 0
}

func (r *Runner) cmdVPN(configPath string, args []string) int {
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(r.Stderr, "usage: maxwell vpn status")
		return 2
	}
	fs := flag.NewFlagSet("vpn status", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	verbose := fs.Bool("verbose", false, "show all vpn signals")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	state, sig, err := svc.VPNStatus(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "vpn status: %v\n", err)
		return 1
	}
	if !*verbose {
		fmt.Fprintf(r.Stdout, "state=%s tunnel=%t default_route=%t iface=%s home=%t home_ip=%t\n", state, sig.HasTunnelInterface, sig.HasDefaultRoute, sig.DefaultRouteInterface, sig.OnHomeNetwork, sig.PublicIPLooksHome)
		return 0
	}
	fmt.Fprintf(r.Stdout, "state=%s\n", state)
	fmt.Fprintf(r.Stdout, "has_tunnel_interface=%t\n", sig.HasTunnelInterface)
	fmt.Fprintf(r.Stdout, "has_default_route=%t\n", sig.HasDefaultRoute)
	fmt.Fprintf(r.Stdout, "default_route_interface=%s\n", sig.DefaultRouteInterface)
	fmt.Fprintf(r.Stdout, "default_interface_ip=%s\n", sig.DefaultInterfaceIP)
	fmt.Fprintf(r.Stdout, "active_ssid=%s\n", sig.ActiveSSID)
	fmt.Fprintf(r.Stdout, "public_ip=%s\n", sig.PublicIP)
	fmt.Fprintf(r.Stdout, "public_asn=%s\n", sig.PublicASN)
	fmt.Fprintf(r.Stdout, "on_home_network=%t\n", sig.OnHomeNetwork)
	fmt.Fprintf(r.Stdout, "public_ip_looks_home=%t\n", sig.PublicIPLooksHome)
	fmt.Fprintf(r.Stdout, "asn_looks_vpn=%t\n", sig.ASNLooksVPN)
	fmt.Fprintf(r.Stdout, "tunnel_interfaces=%s\n", strings.Join(sig.TunnelInterfaces, ","))
	return 0
}

func (r *Runner) cmdTorrents(configPath string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(r.Stderr, "usage: maxwell torrents list|add <magnet>|setup-qbittorrent")
		return 2
	}
	if args[0] == "setup-qbittorrent" {
		return r.cmdTorrentsSetupQBit(configPath, args[1:])
	}
	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()

	switch args[0] {
	case "list":
		list, err := svc.ListTorrents(ctx)
		if err != nil {
			fmt.Fprintf(r.Stderr, "list torrents: %v\n", hintConnErr(err))
			return 1
		}
		for _, t := range list {
			fmt.Fprintf(r.Stdout, "%s\t%s\t%.2f\t%s\n", t.ID, t.Name, t.Progress, t.State)
		}
		return 0
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: maxwell torrents add <magnet>")
			return 2
		}
		id, err := svc.AddMagnet(ctx, args[1])
		if err != nil {
			fmt.Fprintf(r.Stderr, "add magnet: %v\n", hintConnErr(err))
			return 1
		}
		fmt.Fprintf(r.Stdout, "added=%s\n", id)
		return 0
	default:
		fmt.Fprintln(r.Stderr, "usage: maxwell torrents list|add <magnet>|setup-qbittorrent")
		return 2
	}
}

func (r *Runner) cmdQueue(configPath string, args []string) int {
	if len(args) > 1 || (len(args) == 1 && args[0] != "list") {
		fmt.Fprintln(r.Stderr, "usage: maxwell queue [list]")
		return 2
	}
	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	conv, err := svc.ListConversionJobs(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "queue conversion: %v\n", err)
		return 1
	}
	upl, err := svc.ListUploadJobs(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "queue upload: %v\n", err)
		return 1
	}
	fmt.Fprintf(r.Stdout, "conversion_jobs=%d upload_jobs=%d\n", len(conv), len(upl))
	return 0
}

func (r *Runner) cmdLinks(configPath string, args []string) int {
	latest := 0
	if len(args) >= 2 && args[0] == "list" && args[1] == "--latest" {
		latest = 1
	} else if len(args) >= 1 && strings.HasPrefix(args[0], "--latest=") {
		n, _ := strconv.Atoi(strings.TrimPrefix(args[0], "--latest="))
		latest = n
	} else if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(r.Stderr, "usage: maxwell links list [--latest]")
		return 2
	}

	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	links, err := svc.ListLinks(ctx, latest)
	if err != nil {
		fmt.Fprintf(r.Stderr, "list links: %v\n", err)
		return 1
	}
	for _, l := range links {
		fmt.Fprintln(r.Stdout, l.FinalURL)
	}
	return 0
}

func (r *Runner) cmdWeb(configPath string, args []string) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	bind := fs.String("bind", "", "override web bind address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "load config: %v\n", err)
		return 1
	}
	svc, err := r.Build(configPath)
	if err != nil {
		fmt.Fprintf(r.Stderr, "build service: %v\n", err)
		return 1
	}
	defer svc.Close()

	address := cfg.Security.WebBind
	if *bind != "" {
		address = *bind
	}
	server := webui.NewServer(svc, cfg.Security.WebToken, cfg.Security.CSRF)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(r.Stdout, "web listening on %s\n", address)
	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	workerDone := startWebPipelineLoop(workerCtx, svc, 4*time.Second, r.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		workerCancel()
		<-workerDone
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return 0
	case err := <-errCh:
		workerCancel()
		<-workerDone
		fmt.Fprintf(r.Stderr, "web server: %v\n", err)
		return 1
	}
}

func startWebPipelineLoop(ctx context.Context, svc ServiceAPI, interval time.Duration, stderr io.Writer) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		interval = 4 * time.Second
	}
	go func() {
		defer close(done)
		runOne := func() {
			stepCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			if err := svc.SyncCompletedDownloads(stepCtx); err != nil && stderr != nil {
				fmt.Fprintf(stderr, "web pipeline sync: %v\n", err)
			}
			if err := svc.ProcessOnce(stepCtx); err != nil && stderr != nil {
				fmt.Fprintf(stderr, "web pipeline process: %v\n", err)
			}
		}
		runOne()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOne()
			}
		}
	}()
	return done
}

func (r *Runner) printUsage() {
	fmt.Fprintln(r.Stdout, "maxwell --config <path> <command>")
	fmt.Fprintln(r.Stdout, "commands:")
	fmt.Fprintln(r.Stdout, "  doctor")
	fmt.Fprintln(r.Stdout, "  run [--cycles N|--forever --interval 8s --require-safe-vpn]")
	fmt.Fprintln(r.Stdout, "  vpn status")
	fmt.Fprintln(r.Stdout, "  torrents list")
	fmt.Fprintln(r.Stdout, "  torrents add <magnet>")
	fmt.Fprintln(r.Stdout, "  torrents setup-qbittorrent [--port 8080 --start=true --verify=true]")
	fmt.Fprintln(r.Stdout, "  queue [list]")
	fmt.Fprintln(r.Stdout, "  links list [--latest]")
	fmt.Fprintln(r.Stdout, "  web [--bind 127.0.0.1:7777]")
}

func ensureDirWritable(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("empty path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".maxwell-doctor.tmp")
	if err := os.WriteFile(tmp, []byte("ok"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	return nil
}

func checkStore(state config.StateStoreConfig) error {
	s, err := queue.Open(state)
	if err != nil {
		return err
	}
	return s.Close()
}

func checkBinary(bin string, allowCopy bool) error {
	b := strings.TrimSpace(bin)
	if b == "" {
		if !allowCopy {
			return nil
		}
		return fmt.Errorf("binary not configured")
	}
	if allowCopy && strings.EqualFold(b, "copy") {
		return nil
	}
	_, err := exec.LookPath(b)
	return err
}

func checkTorrentAPI(cfg config.TorrentConfig) error {
	c, err := torrent.NewClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.List(ctx)
	return err
}

func hintConnErr(err error) string {
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "connection refused") || strings.Contains(strings.ToLower(msg), "no such host") {
		return msg + " (check that your torrent client is running and torrent.base_url is correct; run `maxwell --config ./config.yaml torrents setup-qbittorrent` then `maxwell --config ./config.yaml doctor`)"
	}
	return msg
}
