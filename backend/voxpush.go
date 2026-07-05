package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PushToVox copies files/folders into the VOX app on a USB-connected iPhone by
// invoking the `vox-push` CLI. It waits for completion and returns the combined
// stdout/stderr so callers can surface errors (e.g. no device connected).
func PushToVox(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("no paths to push")
	}

	// vox-push runs with `set -e` and aborts on the first non-existent path, so
	// drop anything missing up front to avoid one stale path failing the batch.
	existing := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		}
	}
	if len(existing) == 0 {
		return "", fmt.Errorf("none of the %d path(s) exist on disk", len(paths))
	}

	voxPush, err := resolveVoxPush()
	if err != nil {
		return "", err
	}

	cmd := exec.Command(voxPush, existing...)
	cmd.Env = voxPushEnv()

	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if len(trimmed) > 600 {
			trimmed = "…" + trimmed[len(trimmed)-600:]
		}
		if trimmed != "" {
			return string(out), fmt.Errorf("vox-push failed (%v): %s", err, trimmed)
		}
		return string(out), fmt.Errorf("vox-push failed: %v", err)
	}
	return string(out), nil
}

// resolveVoxPush locates the vox-push binary. The packaged .app runs with a
// minimal PATH, so check common install dirs by absolute path before falling
// back to PATH lookup.
func resolveVoxPush() (string, error) {
	for _, c := range []string{"/usr/local/bin/vox-push", "/opt/homebrew/bin/vox-push"} {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath("vox-push"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("vox-push not found (expected in /usr/local/bin)")
}

// voxPushEnv returns the current environment with PATH prepended by the dirs
// where vox-push and its pymobiledevice3 dependency live. Without this the
// packaged .app cannot find pymobiledevice3 (~/.local/bin).
func voxPushEnv() []string {
	home, _ := os.UserHomeDir()
	extra := []string{"/usr/local/bin", "/opt/homebrew/bin", filepath.Join(home, ".local", "bin")}

	env := os.Environ()
	newPath := strings.Join(append(extra, os.Getenv("PATH")), ":")

	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			out = append(out, "PATH="+newPath)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, "PATH="+newPath)
	}
	return out
}
