//go:build linux

package desktop

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

func OpenAdminUI(port string) {
	url := fmt.Sprintf("http://127.0.0.1:%s/admin", port)
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		log.Printf("Failed to open admin UI: %v", err)
	}
}

func openFileInEditor(path string) {
	if err := exec.Command("xdg-open", path).Start(); err != nil {
		log.Printf("Failed to open editor: %v", err)
	}
}

// showInputDialog prompts for a single line of text using zenity or kdialog,
// degrading gracefully (returning an error) when neither is installed.
func showInputDialog(title, prompt, defaultValue string) (string, error) {
	if !isSafeInput(title) || !isSafeInput(prompt) {
		return "", fmt.Errorf("invalid characters in dialog title or prompt")
	}

	safeDefault := defaultValue
	if !isSafeInput(safeDefault) {
		safeDefault = ""
	}

	if _, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command("zenity", "--entry",
			"--title="+title,
			"--text="+prompt,
			"--entry-text="+safeDefault,
		).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}

	if _, err := exec.LookPath("kdialog"); err == nil {
		out, err := exec.Command("kdialog", "--title", title, "--inputbox", prompt, safeDefault).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}

	return "", fmt.Errorf("no dialog helper found (install zenity or kdialog)")
}

func isSafeInput(s string) bool {
	for _, r := range s {
		if r < 32 && r != '\t' {
			return false
		}
	}
	return !strings.ContainsAny(s, "(){}<>|&;`$")
}

// openLogsConsole opens a terminal emulator tailing the proxy log file.
func openLogsConsole() {
	logPath := GetLogFilePath()
	script := fmt.Sprintf("tail -f '%s'", strings.ReplaceAll(logPath, "'", "'\\''"))

	candidates := [][]string{
		{"x-terminal-emulator", "-e", "sh", "-c", script},
		{"gnome-terminal", "--", "sh", "-c", script},
		{"konsole", "-e", "sh", "-c", script},
		{"xfce4-terminal", "-e", "sh -c " + script},
		{"xterm", "-e", "sh", "-c", script},
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		if err := exec.Command(c[0], c[1:]...).Start(); err == nil {
			return
		}
	}
	log.Printf("Failed to open logs console: no terminal emulator found")
}
