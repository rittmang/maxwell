package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"maxwell/internal/config"
)

type qbSetupResult struct {
	INIPath      string
	BaseURL      string
	Port         int
	Username     string
	CreatedINI   bool
	UpdatedINI   bool
	LocalhostByp bool
}

func (r *Runner) cmdTorrentsSetupQBit(configPath string, args []string) int {
	fs := flag.NewFlagSet("torrents setup-qbittorrent", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	port := fs.Int("port", 0, "web ui port override (0 keeps existing or defaults to 8080)")
	start := fs.Bool("start", true, "start qBittorrent app if API is unreachable")
	verify := fs.Bool("verify", true, "verify qBittorrent API connectivity after setup")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfgFile := strings.TrimSpace(configPath)
	if cfgFile == "" {
		cfgFile = "config.yaml"
	}

	cfg, err := loadOrDefaultConfig(cfgFile)
	if err != nil {
		fmt.Fprintf(r.Stderr, "load config: %v\n", err)
		return 1
	}
	cfg.Torrent.Provider = "qbittorrent"
	if strings.TrimSpace(cfg.Torrent.DownloadDir) == "" {
		cfg.Torrent.DownloadDir = "./downloads"
	}
	if strings.TrimSpace(cfg.Torrent.Category) == "" {
		cfg.Torrent.Category = "maxwell"
	}

	setup, err := ensureQBitWebUI(*port)
	if err != nil {
		fmt.Fprintf(r.Stderr, "qbittorrent setup: %v\n", err)
		return 1
	}

	cfg.Torrent.BaseURL = setup.BaseURL
	if strings.TrimSpace(cfg.Torrent.Username) == "" && strings.TrimSpace(setup.Username) != "" {
		cfg.Torrent.Username = setup.Username
	}
	if setup.LocalhostByp {
		cfg.Torrent.Password = ""
	}

	if err := config.Save(cfgFile, cfg); err != nil {
		fmt.Fprintf(r.Stderr, "save config: %v\n", err)
		return 1
	}

	fmt.Fprintf(r.Stdout, "qbittorrent_webui_ini=%s\n", setup.INIPath)
	fmt.Fprintf(r.Stdout, "qbittorrent_base_url=%s\n", setup.BaseURL)
	fmt.Fprintf(r.Stdout, "qbittorrent_localhost_bypass=%t\n", setup.LocalhostByp)
	fmt.Fprintf(r.Stdout, "maxwell_config=%s\n", cfgFile)

	if !*verify {
		fmt.Fprintln(r.Stdout, "qbittorrent_setup=ok verify=skipped")
		return 0
	}

	if *start {
		var err error
		if setup.UpdatedINI {
			err = restartQBitApp()
		} else {
			err = startQBitApp()
		}
		if err != nil {
			fmt.Fprintf(r.Stderr, "start/restart qbittorrent: %v\n", err)
		}
	}

	if err := waitForQBitAPI(cfg.Torrent, 25*time.Second); err != nil {
		fmt.Fprintf(r.Stderr, "qbittorrent api still unreachable: %v\n", hintConnErr(err))
		if isQBitAuthErr(err) {
			fmt.Fprintln(r.Stderr, "qbittorrent authentication is enabled. set torrent.username and torrent.password in config.yaml to your qB WebUI credentials, then rerun doctor.")
		}
		return 1
	}

	fmt.Fprintln(r.Stdout, "qbittorrent_setup=ok api=reachable")
	return 0
}

func loadOrDefaultConfig(path string) (config.Config, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Default(), nil
		}
		return config.Config{}, err
	}
	return config.Load(path)
}

func tryAutoSetupQBit(configPath string) {
	cfgPath := strings.TrimSpace(configPath)
	if cfgPath == "" {
		return
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Torrent.Provider), "qbittorrent") {
		return
	}
	if !isLocalBaseURL(cfg.Torrent.BaseURL) {
		return
	}
	if err := checkQBitTCP(cfg.Torrent.BaseURL, 300*time.Millisecond); err == nil {
		return
	}
	setup, err := ensureQBitWebUI(0)
	if err != nil {
		return
	}
	cfg.Torrent.BaseURL = setup.BaseURL
	if strings.TrimSpace(cfg.Torrent.Username) == "" && strings.TrimSpace(setup.Username) != "" {
		cfg.Torrent.Username = setup.Username
	}
	if setup.LocalhostByp {
		cfg.Torrent.Password = ""
	}
	_ = config.Save(cfgPath, cfg)
}

func ensureQBitWebUI(preferredPort int) (qbSetupResult, error) {
	iniPath, err := findOrCreateQBitINIPath()
	if err != nil {
		return qbSetupResult{}, err
	}
	doc, created, err := loadINIFile(iniPath)
	if err != nil {
		return qbSetupResult{}, err
	}

	port := preferredPort
	if port <= 0 {
		if existing, ok := doc.Get("Preferences", `WebUI\Port`); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(existing)); err == nil && n > 0 {
				port = n
			}
		}
	}
	if port <= 0 {
		port = 8080
	}

	changed := false
	changed = doc.Set("Preferences", `WebUI\Enabled`, "true") || changed
	changed = doc.Set("Preferences", `WebUI\Address`, "127.0.0.1") || changed
	changed = doc.Set("Preferences", `WebUI\Port`, strconv.Itoa(port)) || changed
	changed = doc.Set("Preferences", `WebUI\LocalHostAuth`, "false") || changed
	username, _ := doc.Get("Preferences", `WebUI\Username`)
	if changed {
		if err := os.MkdirAll(filepath.Dir(iniPath), 0o755); err != nil {
			return qbSetupResult{}, fmt.Errorf("create qbittorrent preferences dir: %w", err)
		}
		if err := os.WriteFile(iniPath, []byte(doc.String()), 0o644); err != nil {
			return qbSetupResult{}, fmt.Errorf("write qbittorrent ini: %w", err)
		}
	}

	return qbSetupResult{
		INIPath:      iniPath,
		BaseURL:      fmt.Sprintf("http://127.0.0.1:%d", port),
		Port:         port,
		Username:     strings.TrimSpace(username),
		CreatedINI:   created,
		UpdatedINI:   changed,
		LocalhostByp: true,
	}, nil
}

func waitForQBitAPI(cfg config.TorrentConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := checkQBitTCP(cfg.BaseURL, 700*time.Millisecond); err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		err := checkTorrentAPI(cfg)
		if err == nil {
			return nil
		}
		if isQBitAuthErr(err) {
			return err
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout waiting for qBittorrent API")
	}
	return lastErr
}

func checkQBitTCP(baseURL string, timeout time.Duration) error {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "80")
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func isLocalBaseURL(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

func startQBitApp() error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-a", "qBittorrent")
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", "qBittorrent")
	default:
		cmd = exec.Command("qbittorrent")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func restartQBitApp() error {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("osascript", "-e", `tell application "qBittorrent" to quit`).Run()
		_ = exec.Command("pkill", "-f", "qbittorrent").Run()
		time.Sleep(1200 * time.Millisecond)
		return startQBitApp()
	case "windows":
		_ = exec.Command("taskkill", "/IM", "qbittorrent.exe", "/F").Run()
		time.Sleep(1200 * time.Millisecond)
		return startQBitApp()
	default:
		_ = exec.Command("pkill", "-f", "qbittorrent").Run()
		time.Sleep(1200 * time.Millisecond)
		return startQBitApp()
	}
}

func findOrCreateQBitINIPath() (string, error) {
	candidates := qBitINICandidates()
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if len(candidates) == 0 || candidates[0] == "" {
		return "", fmt.Errorf("could not determine qBittorrent preferences path")
	}
	return candidates[0], nil
}

func qBitINICandidates() []string {
	override := strings.TrimSpace(os.Getenv("MAXWELL_QBITTORRENT_INI"))
	if override != "" {
		return []string{override}
	}
	home, _ := os.UserHomeDir()
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Preferences", "qBittorrent", "qBittorrent.ini"),
		}
	case "windows":
		if appData != "" {
			return []string{filepath.Join(appData, "qBittorrent", "qBittorrent.ini")}
		}
		return []string{filepath.Join(home, "AppData", "Roaming", "qBittorrent", "qBittorrent.ini")}
	default:
		return []string{
			filepath.Join(home, ".config", "qBittorrent", "qBittorrent.conf"),
			filepath.Join(home, ".config", "qBittorrent", "qBittorrent.ini"),
		}
	}
}

type iniDoc struct {
	lines []string
}

func loadINIFile(path string) (*iniDoc, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &iniDoc{lines: []string{}}, true, nil
		}
		return nil, false, err
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return &iniDoc{lines: lines}, false, nil
}

func (d *iniDoc) String() string {
	if len(d.lines) == 0 {
		return ""
	}
	return strings.Join(d.lines, "\n") + "\n"
}

func (d *iniDoc) Get(section, key string) (string, bool) {
	start, end, ok := d.sectionRange(section)
	if !ok {
		return "", false
	}
	for i := start + 1; i < end; i++ {
		k, v, ok := parseINIKeyValue(d.lines[i])
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

func (d *iniDoc) Set(section, key, value string) bool {
	entry := key + "=" + value
	start, end, ok := d.sectionRange(section)
	if !ok {
		if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
			d.lines = append(d.lines, "")
		}
		d.lines = append(d.lines, "["+section+"]", entry)
		return true
	}
	for i := start + 1; i < end; i++ {
		k, _, ok := parseINIKeyValue(d.lines[i])
		if ok && k == key {
			if strings.TrimSpace(d.lines[i]) == entry {
				return false
			}
			d.lines[i] = entry
			return true
		}
	}
	d.lines = append(d.lines[:end], append([]string{entry}, d.lines[end:]...)...)
	return true
}

func (d *iniDoc) sectionRange(section string) (int, int, bool) {
	start := -1
	for i := range d.lines {
		s := strings.TrimSpace(d.lines[i])
		if s == "["+section+"]" {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	end := len(d.lines)
	for i := start + 1; i < len(d.lines); i++ {
		s := strings.TrimSpace(d.lines[i])
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			end = i
			break
		}
	}
	return start, end, true
}

func parseINIKeyValue(line string) (string, string, bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "[") {
		return "", "", false
	}
	idx := strings.IndexByte(s, '=')
	if idx < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:idx])
	val := strings.TrimSpace(s[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

func isQBitAuthErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid credentials") ||
		strings.Contains(msg, "has been banned") ||
		strings.Contains(msg, "qb login failed") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "unauthorized")
}
