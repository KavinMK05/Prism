@echo off
tasklist /FI "IMAGENAME eq prism.exe" 2>NUL | find /I "prism.exe" >NUL
if %ERRORLEVEL%==0 (
    echo Stopping Prism...
    taskkill /IM prism.exe /F >NUL 2>&1
    echo Prism stopped.
) else (
    echo Prism is not running.
)
timeout /t 3 >NUL