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

$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
$TmpZip = "$TmpDir.zip"
New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null
try {
  Invoke-WebRequest -Uri $Url -OutFile $TmpZip -UseBasicParsing
  Expand-Archive -Path $TmpZip -DestinationPath $TmpDir -Force
  Move-Item -Path (Join-Path $TmpDir "$Binary.exe") -Destination (Join-Path $InstallDir "$Binary.exe") -Force
} finally {
  Remove-Item $TmpZip, $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "Installed: $InstallDir\$Binary.exe"

$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($UserPath -notlike "*$InstallDir*") {
  Write-Host ""
  Write-Host "Adding $InstallDir to your user PATH..."
  [Environment]::SetEnvironmentVariable('Path', "$UserPath;$InstallDir", 'User')
  Write-Host "Restart your terminal for PATH changes to take effect."
}

Write-Host ""
Write-Host "GitHub token setup ----------------------------------------------"
Write-Host ""

$ExistingToken = [Environment]::GetEnvironmentVariable('GITHUB_PERSONAL_ACCESS_TOKEN','User')
if ($ExistingToken) {
  Write-Host "GITHUB_PERSONAL_ACCESS_TOKEN is already set in your user environment. Skipping."
} else {
  Write-Host "Create a fine-grained GitHub personal access token with read access to"
  Write-Host "the knowledge base repository you will use:"
  Write-Host "  https://github.com/settings/personal-access-tokens/new"
  Write-Host ""
  $IsInteractive = [Environment]::UserInteractive -and -not [Console]::IsInputRedirected
  if ($IsInteractive) {
    $Token = Read-Host "Paste your token now (or press Enter to skip)"
    if ($Token) {
      [Environment]::SetEnvironmentVariable('GITHUB_PERSONAL_ACCESS_TOKEN', $Token, 'User')
      Write-Host "Saved as user environment variable GITHUB_PERSONAL_ACCESS_TOKEN."
      Write-Host "Open a new terminal so the variable becomes visible."
    } else {
      Write-Host "Skipped. Set it later with:"
      Write-Host "  [Environment]::SetEnvironmentVariable('GITHUB_PERSONAL_ACCESS_TOKEN','<token>','User')"
    }
  } else {
    Write-Host "Non-interactive shell detected. Set the token later with:"
    Write-Host "  [Environment]::SetEnvironmentVariable('GITHUB_PERSONAL_ACCESS_TOKEN','<token>','User')"
  }
}

Write-Host ""
Write-Host "Reference the env var in your MCP config, e.g. neusiscode.json:"
Write-Host '  "environment": { "GITHUB_PERSONAL_ACCESS_TOKEN": "{env:GITHUB_PERSONAL_ACCESS_TOKEN}" }'
Write-Host ""
