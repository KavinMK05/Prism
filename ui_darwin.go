//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func openAdminUI(port string) {
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
	logPath := getLogFilePath()
	script := fmt.Sprintf(`tell application "Terminal"
	activate
	do script "tail -f '%s'"
end tell`, strings.ReplaceAll(logPath, "'", "'\\''"))
	exec.Command("osascript", "-e", script).Start()
}

func openInFileExplorer(path string) {
	exec.Command("open", "-R", path).Start()
}

func editModelConfig() {
	remapPath := getModelRemappingPath()
	if _, err := os.Stat(remapPath); os.IsNotExist(err) {
		remap := defaultModelRemapping()
		saveModelRemapping(remap)
	}

	exec.Command("open", "-t", remapPath).Start()

	go func() {
		initialModTime := getFileModTime(remapPath)
		for i := 0; i < 120; i++ {
			time.Sleep(1 * time.Second)
			currentModTime := getFileModTime(remapPath)
			if !currentModTime.Equal(initialModTime) {
				time.Sleep(500 * time.Millisecond)
				cfg = loadConfig()
				if isProxyRunning() {
					stopProxyProcess()
					time.Sleep(500 * time.Millisecond)
					startProxyProcess()
					time.Sleep(500 * time.Millisecond)
					updateMenu(isProxyRunning())
				}
				return
			}
		}
	}()
}

func getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}