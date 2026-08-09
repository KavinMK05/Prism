//go:build darwin

package desktop

import (
	"fmt"
	"os/exec"
	"strings"
)

func OpenAdminUI(port string) {
	url := fmt.Sprintf("http://127.0.0.1:%s/admin", port)
	exec.Command("open", url).Start()
}

func openFileInEditor(path string) {
	exec.Command("open", "-t", path).Start()
}

func showInputDialog(title, prompt, defaultValue string) (string, error) {
	script := fmt.Sprintf(
		`display dialog "%s" default answer "%s" with title "%s"`,
		escapeAppleScript(prompt),
		escapeAppleScript(defaultValue),
		escapeAppleScript(title),
	)
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(out))
	if idx := strings.Index(result, "text returned:"); idx >= 0 {
		result = result[idx+len("text returned:"):]
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "", nil
	}
	return result, nil
}

func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

func isSafeInput(s string) bool {
	for _, r := range s {
		if r < 32 && r != '\t' {
			return false
		}
	}
	return !strings.ContainsAny(s, "(){}<>|&;`$")
}

func openLogsConsole() {
	logPath := GetLogFilePath()
	script := fmt.Sprintf(`tell application "Terminal"
	activate
	do script "tail -f '%s'"
end tell`, strings.ReplaceAll(logPath, "'", "'\\''"))
	exec.Command("osascript", "-e", script).Start()
}
