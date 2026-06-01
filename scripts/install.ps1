param(
    [string]$Repo = $env:VOXGATE_REPO,
    [string]$Version = $env:VOXGATE_VERSION,
    [string]$InstallDir = $env:VOXGATE_INSTALL_DIR
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($Repo)) {
    $Repo = "WEIFENG2333/voxgate"
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = "latest"
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\voxgate"
    } else {
        $InstallDir = Join-Path ([Environment]::GetFolderPath("LocalApplicationData")) "Programs\voxgate"
    }
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    throw "could not determine install directory; set VOXGATE_INSTALL_DIR"
}

function Get-AssetArch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { return "amd64" }
        "ARM64" { throw "Windows ARM64 release asset is not published yet; set VOXGATE_INSTALL_DIR and install a compatible voxgate.exe manually" }
        default {
            if ([Environment]::Is64BitOperatingSystem) {
                return "amd64"
            }
            throw "unsupported Windows architecture: $env:PROCESSOR_ARCHITECTURE"
        }
    }
}

function Get-LatestVersion {
    try {
        $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "voxgate-installer" }
        if (-not [string]::IsNullOrWhiteSpace($latest.tag_name)) {
            return $latest.tag_name
        }
    } catch {
        # GitHub's API can be rate limited. Fall back to the public redirect below.
    }

    $response = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -MaximumRedirection 10
    $finalUri = $null
    if ($response.BaseResponse.ResponseUri) {
        $finalUri = $response.BaseResponse.ResponseUri.AbsoluteUri
    } elseif ($response.BaseResponse.RequestMessage -and $response.BaseResponse.RequestMessage.RequestUri) {
        $finalUri = $response.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
    }
    if ($finalUri -and ($finalUri -match "/releases/tag/([^/?#]+)")) {
        return $matches[1]
    }
    throw "could not determine latest voxgate release"
}

if ($Version -eq "latest") {
    $Version = Get-LatestVersion
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    throw "could not determine latest voxgate release"
}

$arch = Get-AssetArch
$asset = "voxgate_windows_$arch.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$Version"
$target = Join-Path $InstallDir "voxgate.exe"
if (Test-Path $target) {
    try {
        $current = (& $target version 2>$null) -replace "^voxgate\s+", ""
        if ($current -and (($current -eq $Version) -or ("v$current" -eq $Version))) {
            Write-Host "voxgate $current is already installed at $target"
            exit 0
        }
    } catch {
        # Continue with reinstall if the existing binary cannot report a version.
    }
}
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("voxgate-install-" + [Guid]::NewGuid().ToString("N"))
$zipPath = Join-Path $tmp $asset
$checksumPath = Join-Path $tmp "checksums.txt"
$extractDir = Join-Path $tmp "extract"

New-Item -ItemType Directory -Force -Path $tmp, $extractDir, $InstallDir | Out-Null

try {
    Write-Host "Installing voxgate $Version for windows/$arch"
    Invoke-WebRequest -Uri "$baseUrl/$asset" -OutFile $zipPath
    try {
        Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $checksumPath
        $line = Get-Content $checksumPath | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } | Select-Object -First 1
        if ($line) {
            $expected = ($line -split "\s+")[0].ToLowerInvariant()
            $actual = (Get-FileHash -Algorithm SHA256 $zipPath).Hash.ToLowerInvariant()
            if ($actual -ne $expected) {
                throw "checksum mismatch for $asset"
            }
            Write-Host "${asset}: OK"
        }
    } catch {
        Write-Warning "checksum verification skipped: $($_.Exception.Message)"
    }

    Expand-Archive -Path $zipPath -DestinationPath $extractDir -Force
    $exe = Get-ChildItem -Path $extractDir -Recurse -Filter "voxgate.exe" | Select-Object -First 1
    if (-not $exe) {
        throw "voxgate.exe not found in release archive"
    }
    Copy-Item $exe.FullName $target -Force
    Get-ChildItem -Path $extractDir -Recurse -Filter "*.dll" | ForEach-Object {
        Copy-Item $_.FullName $InstallDir -Force
    }

    Write-Host "Installed: $target"
    if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
        Write-Warning "ffmpeg is not on PATH. Install it with: winget install Gyan.FFmpeg"
    }
    $pathParts = [Environment]::GetEnvironmentVariable("Path", "User") -split ";"
    if ($pathParts -notcontains $InstallDir) {
        Write-Host "Add to current shell PATH:"
        Write-Host "`$env:Path = `"$InstallDir;`$env:Path`""
        Write-Host "Or persist it:"
        Write-Host "[Environment]::SetEnvironmentVariable('Path', `"$InstallDir;`" + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')"
    }
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
