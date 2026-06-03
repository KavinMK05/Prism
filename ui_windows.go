//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func openAdminUI(port string) {
	url := fmt.Sprintf("http://127.0.0.1:%s/admin", port)
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.Start()
}

func openFileInEditor(path string) {
	cmd := exec.Command("notepad", path)
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open editor: %v", err)
	}
}

func showInputDialog(title, prompt, defaultValue string) (string, error) {
	if !isSafeInput(title) || !isSafeInput(prompt) {
		return "", fmt.Errorf("invalid characters in dialog title or prompt")
	}

	safeDefault := defaultValue
	if !isSafeInput(safeDefault) {
		safeDefault = ""
	}

	vbs := fmt.Sprintf(`Dim result
result = InputBox("%s", "%s", "%s")
If result <> "" Then
    WScript.Echo result
End If`,
		escapeVBS(prompt),
		escapeVBS(title),
		escapeVBS(safeDefault),
	)

	tmpVBS := filepath.Join(os.TempDir(), "prism-input.vbs")
	if err := os.WriteFile(tmpVBS, []byte(vbs), 0600); err != nil {
		return "", err
	}
	defer os.Remove(tmpVBS)

	cmd := exec.Command("cscript", "//Nologo", "//E:vbscript", tmpVBS)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", nil
	}
	return result, nil
}

func escapeVBS(s string) string {
	s = strings.ReplaceAll(s, `"`, `""`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
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
	escaped := strings.ReplaceAll(logPath, "'", "''")

	script := fmt.Sprintf(`$path = '%s'
$host.ui.RawUI.WindowTitle = 'Prism Logs'
$lastSize = -1
while ($true) {
    if (Test-Path $path) {
        $item = Get-Item $path
        if ($item.Length -ne $lastSize) {
            $lines = Get-Content $path
            $start = [Math]::Max(0, $lines.Length - 50)
            Clear-Host
            Write-Host '=== Prism Log Viewer ===' -ForegroundColor Cyan
            Write-Host ('File: ' + $path)
            Write-Host ('Size: ' + $item.Length + ' bytes | Lines: ' + $lines.Length)
            Write-Host '========================' -ForegroundColor Cyan
            for ($i = $start; $i -lt $lines.Length; $i++) {
                Write-Host $lines[$i]
            }
            $lastSize = $item.Length
        }
    } else {
        Clear-Host
        Write-Host '=== Prism Log Viewer ===' -ForegroundColor Cyan
        Write-Host 'Waiting for log file...' -ForegroundColor Yellow
        Write-Host ('Expected: ' + $path)
        $lastSize = -1
    }
    Start-Sleep -Milliseconds 500
}
`, escaped)

	tmpPS1 := filepath.Join(os.TempDir(), "prism-logs.ps1")
	os.WriteFile(tmpPS1, []byte(script), 0600)

	cmd := exec.Command("cmd", "/c", "start", "powershell", "-NoExit", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", tmpPS1)
	cmd.Start()
}

func openInFileExplorer(path string) {
	exec.Command("explorer", path).Start()
}

func editModelConfig() {
	remapPath := getModelRemappingPath()
	if _, err := os.Stat(remapPath); os.IsNotExist(err) {
		remap := defaultModelRemapping()
		saveModelRemapping(remap)
	}

	cmd := exec.Command("notepad", remapPath)
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open model config editor: %v", err)
		return
	}

	go func() {
		cmd.Wait()
		cfg = loadConfig()
		if isProxyRunning() {
			stopProxyProcess()
			time.Sleep(500 * time.Millisecond)
			startProxyProcess()
			time.Sleep(500 * time.Millisecond)
			updateMenu(isProxyRunning())
		}
	}()
}