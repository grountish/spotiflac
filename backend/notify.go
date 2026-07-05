package backend

import (
	"os/exec"
	"runtime"
	"strconv"
)

// Notify shows a native desktop notification. Best-effort and non-blocking: the
// command is started and reaped in the background so a slow or missing notifier
// never stalls the caller. Errors are returned for logging but are usually safe
// to ignore.
func Notify(title, body string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		// osascript lives at /usr/bin, so it resolves even under the minimal PATH
		// of a packaged .app. strconv.Quote yields AppleScript-safe quoted strings.
		script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote(title)
		cmd = exec.Command("/usr/bin/osascript", "-e", script)
	case "windows":
		ps := "[reflection.assembly]::loadwithpartialname('System.Windows.Forms')|Out-Null;" +
			"$n=New-Object System.Windows.Forms.NotifyIcon;" +
			"$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;" +
			"$n.ShowBalloonTip(5000," + strconv.Quote(title) + "," + strconv.Quote(body) +
			",[System.Windows.Forms.ToolTipIcon]::Info)"
		cmd = exec.Command("powershell", "-NoProfile", "-Command", ps)
	default:
		p, err := exec.LookPath("notify-send")
		if err != nil {
			return nil
		}
		cmd = exec.Command(p, title, body)
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap the child so it doesn't linger as a zombie
	return nil
}
