@echo off
rem Switch the console to UTF-8, otherwise the Chinese in this file will bemojibake in a GBK terminal.
chcp 65001 >nul 2>&1
rem ARTEX Daemon startup script (Windows)
rem
rem Usage:
rem start.bat Run in the foreground (Ctrl-C to stop)
rem start.bat -addr:9000 Extra parameters are passed through to artex as is
rem
rem It only does one thing: transfer artex.exe Run it. After the process exits, decide whether to restart based on the exit code.
rem
rem 0 User normal stop -> Break Loop,
rem 75 Program requests restart -> Restart immediately (page clicked)"One-click update"Or"Rollback")
rem Other Crash -> Retry after backoff (1->2->4...Maximum of 60 seconds)
rem
rem Downloading, SHA256 verification, and reinstallation are not done here, all by artex itself at startup
rem (selfupdate). Scripts should remain simple. See start.sh The top explanation.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] Executable file not found %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] Normal Exit,
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback ready: artex will complete reinstallation at startup after rerunning.
	echo [artex] Request restart (apply new version)....
	set /a delay=1
	goto loop
)

echo [artex] Abnormal Exit, ^(code=!code!^),!delay!s Restart 1>&2
rem timeout In the redirected console, it will fail; use ping as a fallback (delay N seconds requires N+1) times.
set /a pings=!delay!+1
ping -n!pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if!delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
