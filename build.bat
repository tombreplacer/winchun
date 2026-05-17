@echo off
echo Building winchun.exe...

if not exist "src\winchun.syso" (
    echo Generating Windows manifest...
    cmd /c "go run github.com/akavel/rsrc@latest -manifest winchun.manifest -o src/winchun.syso"
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

