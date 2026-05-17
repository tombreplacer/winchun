@echo off
echo Building winchun.exe...

if not exist "winchun.ico" (
    if exist "winchun.png" (
        echo Generating winchun.ico from winchun.png...
        go run src/tools/png2ico.go winchun.png winchun.ico
    )
)

if not exist "src\winchun.syso" (
    echo Generating Windows manifest and resources...
    if exist "winchun.ico" (
        cmd /c "go run github.com/akavel/rsrc@latest -manifest winchun.manifest -ico winchun.ico -o src/winchun.syso"
    ) else (
        cmd /c "go run github.com/akavel/rsrc@latest -manifest winchun.manifest -o src/winchun.syso"
    )
)

go build -o winchun.exe ./src
if %ERRORLEVEL% neq 0 (
    echo.
    echo [x] Build failed!
    pause
    exit /b %ERRORLEVEL%
)
echo.
echo [v] Build successful: winchun.exe
echo You can now run it directly (it will ask for Administrator automatically)!

