package backend

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	managedSlskdPort           = 5030
	managedSlskdListenPort     = 50300
	managedSlskdRepoFolderName = "slskd"
	managedSlskdStartupTimeout = 45 * time.Second
)

type ManagedSlskdInfo struct {
	Available    bool   `json:"available"`
	Managed      bool   `json:"managed"`
	Running      bool   `json:"running"`
	RepoDir      string `json:"repo_dir,omitempty"`
	AppDir       string `json:"app_dir,omitempty"`
	URL          string `json:"url,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	DownloadsDir string `json:"downloads_dir,omitempty"`
	Error        string `json:"error,omitempty"`
}

type managedSlskdState struct {
	mu    sync.Mutex
	info  ManagedSlskdInfo
	cmd   *exec.Cmd
	log   *os.File
	start sync.Once
	stop  sync.Once
}

var globalManagedSlskd = &managedSlskdState{}

func GetManagedSlskdInfo() ManagedSlskdInfo {
	globalManagedSlskd.mu.Lock()
	defer globalManagedSlskd.mu.Unlock()
	return globalManagedSlskd.info
}

func ApplyManagedSlskdDefaults(settings map[string]interface{}) map[string]interface{} {
	if settings == nil {
		settings = map[string]interface{}{}
	}

	info := GetManagedSlskdInfo()
	if !info.Managed || info.URL == "" || info.APIKey == "" || info.DownloadsDir == "" {
		return settings
	}

	if strings.TrimSpace(stringSetting(settings, "slskdURL")) == "" {
		settings["slskdURL"] = info.URL
	}
	if strings.TrimSpace(stringSetting(settings, "slskdAPIKey")) == "" {
		settings["slskdAPIKey"] = info.APIKey
	}
	if strings.TrimSpace(stringSetting(settings, "slskdDownloadsDir")) == "" {
		settings["slskdDownloadsDir"] = info.DownloadsDir
	}

	return settings
}

func StartManagedSlskd() {
	globalManagedSlskd.start.Do(func() {
		globalManagedSlskd.startManagedProcess()
	})
}

func StopManagedSlskd() {
	globalManagedSlskd.stop.Do(func() {
		globalManagedSlskd.stopManagedProcess()
	})
}

func (m *managedSlskdState) startManagedProcess() {
	info := m.resolveManagedInfo()

	m.mu.Lock()
	m.info = info
	m.mu.Unlock()

	if !info.Available || info.Error != "" {
		return
	}

	if isHTTPReachable(info.URL) {
		m.mu.Lock()
		m.info.Running = true
		m.mu.Unlock()
		return
	}

	cmd, err := buildManagedSlskdCommand(info)
	if err != nil {
		m.mu.Lock()
		m.info.Error = err.Error()
		m.mu.Unlock()
		return
	}

	logFile, logErr := os.OpenFile(filepath.Join(info.AppDir, "slskd-managed.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logErr != nil {
		m.mu.Lock()
		m.info.Error = fmt.Sprintf("failed to open managed slskd log: %v", logErr)
		m.mu.Unlock()
		return
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		m.mu.Lock()
		m.info.Error = fmt.Sprintf("failed to start managed slskd: %v", err)
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	m.cmd = cmd
	m.log = logFile
	m.mu.Unlock()

	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cmd = nil
		if m.log != nil {
			_ = m.log.Close()
			m.log = nil
		}
		m.info.Running = false
		if err != nil && m.info.Error == "" {
			m.info.Error = fmt.Sprintf("managed slskd exited: %v", err)
		}
	}()

	deadline := time.Now().Add(managedSlskdStartupTimeout)
	for time.Now().Before(deadline) {
		if isHTTPReachable(info.URL) {
			m.mu.Lock()
			m.info.Running = true
			m.mu.Unlock()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	m.mu.Lock()
	if m.info.Error == "" {
		m.info.Error = fmt.Sprintf("managed slskd did not become reachable at %s within %s", info.URL, managedSlskdStartupTimeout)
	}
	m.mu.Unlock()
}

func (m *managedSlskdState) stopManagedProcess() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	if m.log != nil {
		_ = m.log.Close()
		m.log = nil
	}
	m.info.Running = false
}

func (m *managedSlskdState) resolveManagedInfo() ManagedSlskdInfo {
	info := ManagedSlskdInfo{
		Managed: false,
		URL:     fmt.Sprintf("http://127.0.0.1:%d", managedSlskdPort),
	}

	repoDir, err := findManagedSlskdRepo()
	if err != nil {
		info.Error = err.Error()
		return info
	}
	if repoDir == "" {
		return info
	}

	appDir, err := EnsureAppDir()
	if err != nil {
		info.Error = err.Error()
		return info
	}

	managedAppDir := filepath.Join(appDir, "slskd")
	downloadsDir := filepath.Join(managedAppDir, "downloads")
	incompleteDir := filepath.Join(managedAppDir, "incomplete")
	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		info.Error = fmt.Sprintf("failed to create slskd downloads dir: %v", err)
		return info
	}
	if err := os.MkdirAll(incompleteDir, 0o755); err != nil {
		info.Error = fmt.Sprintf("failed to create slskd incomplete dir: %v", err)
		return info
	}

	apiKeyPath := filepath.Join(managedAppDir, "api-key.txt")
	apiKey, err := ensureManagedAPIKey(apiKeyPath)
	if err != nil {
		info.Error = err.Error()
		return info
	}

	info.Available = true
	info.Managed = true
	info.RepoDir = repoDir
	info.AppDir = managedAppDir
	info.APIKey = apiKey
	info.DownloadsDir = downloadsDir
	return info
}

func findManagedSlskdRepo() (string, error) {
	searchRoots := make([]string, 0, 4)
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		searchRoots = append(searchRoots, cwd)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		searchRoots = append(searchRoots, filepath.Dir(exe))
	}

	seen := map[string]bool{}
	for _, root := range searchRoots {
		current := filepath.Clean(root)
		for {
			if seen[current] {
				break
			}
			seen[current] = true

			candidates := []string{
				filepath.Join(current, "third_party", managedSlskdRepoFolderName),
				filepath.Join(current, "frontend", "third_party", managedSlskdRepoFolderName),
			}
			for _, candidate := range candidates {
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					return candidate, nil
				}
			}

			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	return "", nil
}

func ensureManagedAPIKey(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) >= 16 {
			return key, nil
		}
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to generate slskd API key: %w", err)
	}
	key := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("failed to create slskd app dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return "", fmt.Errorf("failed to persist slskd API key: %w", err)
	}
	return key, nil
}

func buildManagedSlskdCommand(info ManagedSlskdInfo) (*exec.Cmd, error) {
	env := append(os.Environ(),
		fmt.Sprintf("APP_DIR=%s", info.AppDir),
		fmt.Sprintf("SLSKD_HTTP_PORT=%d", managedSlskdPort),
		"SLSKD_HTTP_IP_ADDRESS=127.0.0.1",
		"SLSKD_NO_HTTPS=true",
		fmt.Sprintf("SLSKD_API_KEY=%s", info.APIKey),
		fmt.Sprintf("SLSKD_DOWNLOADS_DIR=%s", info.DownloadsDir),
		fmt.Sprintf("SLSKD_INCOMPLETE_DIR=%s", filepath.Join(info.AppDir, "incomplete")),
		fmt.Sprintf("SLSKD_SLSK_LISTEN_PORT=%d", managedSlskdListenPort),
		"SLSKD_REMOTE_CONFIGURATION=true",
	)

	if executable := findManagedSlskdExecutable(info.RepoDir); executable != "" {
		cmd := exec.Command(executable)
		cmd.Dir = filepath.Dir(executable)
		cmd.Env = env
		return cmd, nil
	}

	projectPath := filepath.Join(info.RepoDir, "src", "slskd", "slskd.csproj")
	if _, err := os.Stat(projectPath); err == nil {
		dotnetPath, lookErr := exec.LookPath("dotnet")
		if lookErr != nil {
			return nil, fmt.Errorf("managed slskd repo found at %s but no binary was found and dotnet is not installed", info.RepoDir)
		}
		cmd := exec.Command(dotnetPath, "run", "--project", projectPath, "--configuration", "Release", "--no-launch-profile")
		cmd.Dir = info.RepoDir
		cmd.Env = env
		return cmd, nil
	}

	return nil, fmt.Errorf("managed slskd repo found at %s but no runnable binary or project was found", info.RepoDir)
}

func findManagedSlskdExecutable(repoDir string) string {
	paths := []string{
		filepath.Join(repoDir, "slskd"),
		filepath.Join(repoDir, "slskd.exe"),
		filepath.Join(repoDir, "bin", "slskd"),
		filepath.Join(repoDir, "bin", "slskd.exe"),
		filepath.Join(repoDir, "src", "slskd", "bin", "Release", "net10.0", platformExecutableName("slskd")),
		filepath.Join(repoDir, "src", "slskd", "bin", "Release", "net10.0", "publish", platformExecutableName("slskd")),
		filepath.Join(repoDir, "src", "slskd", "bin", "Debug", "net10.0", platformExecutableName("slskd")),
		filepath.Join(repoDir, "src", "slskd", "bin", "Debug", "net10.0", "publish", platformExecutableName("slskd")),
	}

	for _, candidate := range paths {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if runtime.GOOS == "windows" || info.Mode()&0o111 != 0 {
				return candidate
			}
		}
	}

	return ""
}

func platformExecutableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func isHTTPReachable(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(trimmed)
	if err == nil {
		_ = resp.Body.Close()
		return true
	}

	hostPort := strings.TrimPrefix(trimmed, "http://")
	hostPort = strings.TrimPrefix(hostPort, "https://")
	conn, dialErr := net.DialTimeout("tcp", hostPort, 1*time.Second)
	if dialErr != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func stringSetting(settings map[string]interface{}, key string) string {
	if settings == nil {
		return ""
	}
	value, _ := settings[key].(string)
	return value
}

func ManagedSlskdStartupSummary() string {
	info := GetManagedSlskdInfo()
	if !info.Managed {
		return ""
	}

	buffer := bytes.NewBufferString("managed slskd")
	if info.RepoDir != "" {
		buffer.WriteString(" repo=")
		buffer.WriteString(info.RepoDir)
	}
	if info.URL != "" {
		buffer.WriteString(" url=")
		buffer.WriteString(info.URL)
	}
	if info.Error != "" {
		buffer.WriteString(" error=")
		buffer.WriteString(info.Error)
	}
	return buffer.String()
}