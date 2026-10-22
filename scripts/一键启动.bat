@echo off
setlocal
cd /d "%~dp0"
set "NCMM_EXE=%~dp0ncmm.exe"
set "NCMM_HOME_DIR=%~dp0."
set "NCMM_WEB_PRESERVE_LEGACY_SCHEDULES=true"

if not exist "%NCMM_EXE%" (
    echo [ERROR] ncmm.exe was not found in "%~dp0".
    pause
    exit /b 1
)

:: 后台启动命令确认当前实例就绪后才返回。
"%NCMM_EXE%" --home "%NCMM_HOME_DIR%" web --background
if errorlevel 1 (
    echo [ERROR] WebUI failed to start. Check the output above.
    pause
    exit /b 1
)

:: 打开浏览器访问
start "" "http://127.0.0.1:3899"
exit /b 0
