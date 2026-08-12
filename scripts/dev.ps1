$ErrorActionPreference = 'Stop'

if (-not $env:MUSIC_SERVER_ADDRESS) { $env:MUSIC_SERVER_ADDRESS = ':4533' }
if (-not $env:MUSIC_SERVER_DATA_DIR) { $env:MUSIC_SERVER_DATA_DIR = './.local/data' }
if (-not $env:MUSIC_SERVER_DATABASE_PATH) { $env:MUSIC_SERVER_DATABASE_PATH = './.local/data/music.db' }
if (-not $env:MUSIC_SERVER_MUSIC_DIR) { $env:MUSIC_SERVER_MUSIC_DIR = './.local/music' }
if (-not $env:MUSIC_SERVER_LIBRARY_NAME) { $env:MUSIC_SERVER_LIBRARY_NAME = 'Local Music' }
if (-not $env:MUSIC_SERVER_ADMIN_USERNAME) { $env:MUSIC_SERVER_ADMIN_USERNAME = 'admin' }
if (-not $env:MUSIC_SERVER_ADMIN_PASSWORD) { $env:MUSIC_SERVER_ADMIN_PASSWORD = 'admin' }
if (-not $env:MUSIC_SERVER_API_TOKEN) {
    $env:MUSIC_SERVER_API_TOKEN = 'dev-' + [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
}
if (-not $env:MUSIC_SERVER_MEDIA_TOKEN) {
    $env:MUSIC_SERVER_MEDIA_TOKEN = 'media-' + [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
}
if (-not $env:MUSIC_SERVER_COOKIE_SECURE) { $env:MUSIC_SERVER_COOKIE_SECURE = 'false' }
if (-not $env:MUSIC_SERVER_LOG_LEVEL) { $env:MUSIC_SERVER_LOG_LEVEL = 'debug' }

New-Item -ItemType Directory -Force $env:MUSIC_SERVER_DATA_DIR, $env:MUSIC_SERVER_MUSIC_DIR | Out-Null
Write-Host 'Local API and media tokens were loaded from the environment or generated for this process.'

go run ./cmd/server
