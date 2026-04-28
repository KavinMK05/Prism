@echo off
tasklist /FI "IMAGENAME eq ollama-proxy.exe" 2>NUL | find /I "ollama-proxy.exe" >NUL
if %ERRORLEVEL%==0 (
    echo Stopping ollama-proxy...
    taskkill /IM ollama-proxy.exe /F >NUL 2>&1
    echo ollama-proxy stopped.
) else (
    echo ollama-proxy is not running.
)
timeout /t 3 >NUL