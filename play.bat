@echo off
REM Asteroid Salvage - one-click launcher.
REM Builds the server, then starts the game and the tuning console.
REM HOST A GAME in the menu launches the server itself, with your settings; closing the
REM game shuts it down again.

setlocal
cd /d "%~dp0"

set "PATH=C:\Program Files\Go\bin;%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin;%PATH%"
set CGO_ENABLED=1

echo.
echo   Asteroid Salvage
echo   ================
echo.

REM Stop a previous run so the port is free.
taskkill /f /im server.exe >nul 2>&1

if not exist "bin" mkdir bin

echo   building server...
pushd server
go build -o ..\bin\server.exe .
if errorlevel 1 (
    echo.
    echo   BUILD FAILED - see the errors above.
    popd
    pause
    exit /b 1
)
popd

REM No server is started here on purpose.
REM
REM HOST A GAME in the menu starts one *with the settings on that screen*. When this
REM script also pre-started a server on the same port, the menu's copy failed to bind and
REM died, and the game silently connected to the pre-started one instead — so round
REM length, team count and every other host setting did nothing. The menu owns the server.

echo   starting game...
start "" pythonw client\main.py --name Pilot

echo   starting tuning console...
start "" pythonw tools\devconsole\main.py

echo.
echo   Click HOST A GAME to start a match with your chosen settings.
echo   The tuning console connects by itself once the game is hosted.
echo.
echo   Controls:
echo     W / S            thrust forward and back
echo     A / D            strafe left and right
echo     MOUSE            aim
echo     LEFT-CLICK       fire the laser
echo     HOLD RIGHT-CLICK grab and pull
echo     HOLD SHIFT       boost (yellow bar is your tank)
echo     SPACE            brake
echo     TAB              release the mouse     ESC  pause menu
echo.
echo   Fly out, hold right-click on a rock, haul it home to YOUR mothership.
echo   Shoot rival ships and the huge rocks that are too big to tow.
echo   Drag grab.reaction_scale in the console to change how heavy it feels.
echo.
ping -n 6 127.0.0.1 >nul
