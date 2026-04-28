@echo off
echo === ollama-proxy console log ===
echo.
if exist "%APPDATA%\ollama-proxy\proxy.log" (
    echo Showing last 50 lines of proxy log:
    echo.
    powershell -Command "Get-Content '%APPDATA%\ollama-proxy\proxy.log' -Tail 50 -Wait"
) else (
    echo No log file found at %APPDATA%\ollama-proxy\proxy.log
    echo.
    echo Checking if proxy is running...
    tasklist /FI "IMAGENAME eq ollama-proxy.exe" 2>NUL | find /I "ollama-proxy.exe" >NUL
    if %ERRORLEVEL%==0 (
        echo ollama-proxy IS running but logging to console (not file).
        echo Start proxy with start-proxy.bat to see console output.
    ) else (
        echo ollama-proxy is NOT running.
    )
)
pause