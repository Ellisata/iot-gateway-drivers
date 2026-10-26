@echo off
:: ============================================
::  Build every official driver, cross-compiled for the gateway's target
::  platforms. Windows counterpart of build.sh -- same behavior and options.
::
::  Usage:
::    build.bat                       all platforms (default)
::    build.bat windows               windows/amd64 only
::    build.bat linux                 linux/amd64 only
::    build.bat arm64                 linux/arm64 only
::    build.bat arm32                 linux/arm (GOARM=7) only
::    build.bat native                current platform only
::    build.bat <target> [out-dir]    single platform into an explicit directory
::    build.bat [out-dir]             legacy: native build into <out-dir>
::
::    set VERSION=v0.2.0 & build.bat  override the injected Meta.Version
::
::  Output directories -- one per platform, because every driver has the same
::  file name (iot-driver-<name>[.exe]) on every platform: a shared directory
::  would silently overwrite one architecture with another, and the gateway
::  would skip every mismatched plugin without reporting an error.
::
::    linux/amd64   -> plugins-linux\        windows/amd64 -> plugins-windows\
::    linux/arm64   -> plugins-linux-arm64\  native        -> plugins\
::    linux/arm(v7) -> plugins-linux-arm\
::
::  Copy the matching directory's contents into the gateway's plugins\ directory
::  (next to the gateway executable). Plugins must match the gateway's
::  GOOS/GOARCH and GOARM -- a wrong-architecture plugin is silently skipped.
::
::  arm32 is pinned to GOARM=7 (ARMv7-A + VFPv3). For older boards (ARMv6/ARMv5,
::  e.g. Raspberry Pi 1 / Zero) use: set GOARM=5 ^& build.bat arm32
:: ============================================
setlocal

set "ROOT=%~dp0"

where go >nul 2>&1
if errorlevel 1 (
    echo [FAIL] go not found in PATH
    goto :fail
)

set "TARGET="
set "OUT="

:parse_args
if "%~1"=="" goto :args_done
if /i "%~1"=="-o" goto :parse_out
if /i "%~1"=="--out" goto :parse_out
call :is_target "%~1"
if not errorlevel 1 (
    if defined TARGET (
        echo unexpected argument: %~1
        goto :usage_error
    )
    set "TARGET=%~1"
    shift
    goto :parse_args
)
if defined OUT (
    echo unexpected argument: %~1
    goto :usage_error
)
set "OUT=%~1"
shift
goto :parse_args

:parse_out
if defined OUT (
    echo -o/--out given more than once
    goto :usage_error
)
if "%~2"=="" (
    echo missing value for %~1
    goto :usage_error
)
set "OUT=%~2"
shift
shift
goto :parse_args

:args_done
if defined TARGET goto :normalize
if defined OUT (
    set "TARGET=native"
) else (
    set "TARGET=all"
)

:normalize
if /i "%TARGET%"=="win" set "TARGET=windows"
if /i "%TARGET%"=="arm7" set "TARGET=arm32"
if /i "%TARGET%"=="arm" set "TARGET=arm32"
if /i "%TARGET%"=="all" if defined OUT goto :err_all_out

if defined VERSION goto :version_ready
pushd "%ROOT%"
:: NOTE: the '=' in --abbrev^=0 is escaped on purpose. Inside a FOR /F command
:: string cmd treats a bare '=' as a separator, so the unescaped form would
:: reach git as "--abbrev 0" and fail with "Not a valid object name 0".
for /f "delims=" %%v in ('git describe --tags --abbrev^=0 2^>nul') do set "VERSION=%%v"
popd
if not defined VERSION set "VERSION=dev"
:version_ready

set "ARM_GOARM=%GOARM%"
if not defined ARM_GOARM set "ARM_GOARM=7"

echo version: %VERSION%

if /i "%TARGET%"=="all" goto :run_all
if /i "%TARGET%"=="native" goto :run_native
goto :run_single

:run_all
call :build_platform linux amd64
if errorlevel 1 goto :fail
call :build_platform linux arm64
if errorlevel 1 goto :fail
call :build_platform linux arm "%ARM_GOARM%"
if errorlevel 1 goto :fail
call :build_platform windows amd64
if errorlevel 1 goto :fail
goto :done

:run_native
for /f "delims=" %%i in ('go env GOOS') do set "NATIVE_GOOS=%%i"
for /f "delims=" %%i in ('go env GOARCH') do set "NATIVE_GOARCH=%%i"
set "NATIVE_GOARM=%GOARM%"
if /i "%NATIVE_GOARCH%"=="arm" if not defined NATIVE_GOARM set "NATIVE_GOARM=7"
set "NATIVE_OUT=%ROOT%plugins"
if defined OUT set "NATIVE_OUT=%OUT%"
call :build_one "%NATIVE_GOOS%" "%NATIVE_GOARCH%" "%NATIVE_OUT%" "%NATIVE_GOARM%"
if errorlevel 1 goto :fail
goto :done

:run_single
if /i "%TARGET%"=="windows" call :build_platform windows amd64
if /i "%TARGET%"=="linux" call :build_platform linux amd64
if /i "%TARGET%"=="arm64" call :build_platform linux arm64
if /i "%TARGET%"=="arm32" call :build_platform linux arm "%ARM_GOARM%"
if errorlevel 1 goto :fail
goto :done

:done
echo.
echo done: %TARGET% built with version %VERSION%
endlocal
exit /b 0

:err_all_out
echo error: an explicit output dir needs a single target ^(try: build.bat native "%OUT%"^)
endlocal
exit /b 1

:usage_error
echo.
echo usage: build.bat [all^|windows^|linux^|arm64^|arm32^|native] [out-dir]
endlocal
exit /b 1

:fail
echo.
echo [FAIL] build failed
endlocal
exit /b 1

:: build_platform <GOOS> <GOARCH> [GOARM]
:: Defaults the output dir per platform; an explicit OUT overrides it.
:build_platform
setlocal
set "BP_GOOS=%~1"
set "BP_ARCH=%~2"
set "BP_GOARM=%~3"
set "BP_OUT=%ROOT%plugins-%BP_GOOS%-%BP_ARCH%"
if /i "%BP_ARCH%"=="amd64" set "BP_OUT=%ROOT%plugins-%BP_GOOS%"
if defined OUT set "BP_OUT=%OUT%"
call :build_one "%BP_GOOS%" "%BP_ARCH%" "%BP_OUT%" "%BP_GOARM%"
set "BP_RC=%ERRORLEVEL%"
endlocal & exit /b %BP_RC%

:: build_one <GOOS> <GOARCH> <OUTDIR> [GOARM]
:: Compiles every top-level <name>\main.go into OUTDIR\iot-driver-<name>[.exe].
:build_one
setlocal enabledelayedexpansion
set "BO_GOOS=%~1"
set "BO_ARCH=%~2"
set "BO_OUT=%~3"
set "BO_GOARM=%~4"
set "BO_EXT="
if /i "%BO_GOOS%"=="windows" set "BO_EXT=.exe"

if not exist "%BO_OUT%" mkdir "%BO_OUT%"
pushd "%BO_OUT%"
set "BO_OUT=%CD%"
popd

:: Drop previous outputs: a removed or renamed driver must not linger as a
:: stale, differently-versioned plugin next to the freshly built ones.
del /q "%BO_OUT%\iot-driver-*" 2>nul

set "BO_LABEL=!BO_GOOS!/!BO_ARCH!"
if defined BO_GOARM set "BO_LABEL=!BO_LABEL!/!BO_GOARM!"
echo   [!BO_LABEL!] -^> !BO_OUT!

set "BO_BUILT=0"
pushd "%ROOT%"
:: CGO_ENABLED=0 is required for cross-compiling; GOARM is cleared for every
:: platform except 32-bit ARM, otherwise a host-level GOARM would leak.
set "CGO_ENABLED=0"
set "GOOS=!BO_GOOS!"
set "GOARCH=!BO_ARCH!"
set "GOARM=!BO_GOARM!"

for /d %%d in (*) do (
    if exist "%%d\main.go" (
        set "BO_NAME=%%~nxd"
        go build -ldflags="-s -w -X main.version=!VERSION!" -o "!BO_OUT!\iot-driver-!BO_NAME!!BO_EXT!" "./!BO_NAME!"
        if errorlevel 1 (
            echo   [FAIL] !BO_GOOS!/!BO_ARCH!: iot-driver-!BO_NAME!!BO_EXT! build failed
            popd
            endlocal
            exit /b 1
        )
        echo     [OK] iot-driver-!BO_NAME!!BO_EXT!
        set /a BO_BUILT+=1
    )
)
popd

:: Zero plugins is always a broken environment, never a valid result.
if !BO_BUILT!==0 (
    echo   [FAIL] !BO_GOOS!/!BO_ARCH!: no driver was built
    endlocal
    exit /b 1
)
echo   [OK] !BO_BUILT! drivers for !BO_GOOS!/!BO_ARCH!
endlocal
exit /b 0

:: is_target <arg> -- exit /b 0 when arg is a known target name.
:is_target
if /i "%~1"=="all" exit /b 0
if /i "%~1"=="windows" exit /b 0
if /i "%~1"=="win" exit /b 0
if /i "%~1"=="linux" exit /b 0
if /i "%~1"=="arm64" exit /b 0
if /i "%~1"=="arm32" exit /b 0
if /i "%~1"=="arm7" exit /b 0
if /i "%~1"=="arm" exit /b 0
if /i "%~1"=="native" exit /b 0
exit /b 1
