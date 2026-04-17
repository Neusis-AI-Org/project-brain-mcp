# mcp-project-brain installer for Windows.
# Usage:
#   iwr -useb https://neusis-ai-org.github.io/project-brain-mcp/install.ps1 | iex
# Environment variables:
#   $env:VERSION      Pin a specific version (default: latest release).
#   $env:INSTALL_DIR  Install location (default: $env:USERPROFILE\bin).

$ErrorActionPreference = 'Stop'

$Repo    = 'Neusis-AI-Org/project-brain-mcp'
$Binary  = 'mcp-project-brain'
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { "$env:USERPROFILE\bin" }

$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'x86_64' }
  'ARM64' { 'arm64'  }
  'x86'   { 'i386'   }
  default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

if ($env:VERSION) {
  $Version = $env:VERSION
} else {
  $Version = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name -replace '^v', ''
}

$Archive = "${Binary}_Windows_${Arch}.zip"
$Url     = "https://github.com/$Repo/releases/download/v${Version}/${Archive}"

Write-Host "Downloading $Binary v$Version for Windows/$Arch..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$TmpZip = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName() + '.zip')
Invoke-WebRequest -Uri $Url -OutFile $TmpZip -UseBasicParsing
Expand-Archive -Path $TmpZip -DestinationPath $InstallDir -Force
Remove-Item $TmpZip -Force

Write-Host "Installed: $InstallDir\$Binary.exe"

$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($UserPath -notlike "*$InstallDir*") {
  Write-Host ""
  Write-Host "Adding $InstallDir to your user PATH..."
  [Environment]::SetEnvironmentVariable('Path', "$UserPath;$InstallDir", 'User')
  Write-Host "Restart your terminal for PATH changes to take effect."
}
