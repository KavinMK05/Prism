@echo off
echo === Prism console log ===
echo.
if exist "%APPDATA%\prism\proxy.log" (
    echo Showing last 50 lines of proxy log:
    echo.
    powershell -Command "Get-Content '%APPDATA%\prism\proxy.log' -Tail 50 -Wait"
) else (
    echo No log file found at %APPDATA%\prism\proxy.log
    echo.
    echo Checking if proxy is running...
    tasklist /FI "IMAGENAME eq prism.exe" 2>NUL | find /I "prism.exe" >NUL
    if %ERRORLEVEL%==0 (
        echo Prism IS running but logging to console (not file).
        echo Start proxy with start-proxy.bat to see console output.
    ) else (
        echo Prism is NOT running.
    )
)
pause