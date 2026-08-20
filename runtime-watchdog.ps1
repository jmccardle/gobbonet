# ==============================================================================
# runtime-watchdog.ps1 -- clean up background services owned by one launch.bat
#
# This variant targets current GobboNet 1.5.8:
#   - web UI default: 9066
#   - llama.cpp:      11437
#   - embeddings:     11436
#   - search proxy:   11435 (PowerShell -EncodedCommand)
#
# The launcher writes an ownership state file. When the dedicated launcher
# cmd.exe exits, this watchdog stops only services that this invocation marked
# as owned. Pre-existing compatible services are left alone.
# ==============================================================================

$ErrorActionPreference = 'SilentlyContinue'

function Env([string]$Name, $Default = '') {
	$v = [Environment]::GetEnvironmentVariable($Name)
	if ([string]::IsNullOrWhiteSpace($v)) { return $Default }
	return $v
}

$ParentPid = [int](Env 'GOBBONET_PARENT_PID' '0')
$StateFile = Env 'GOBBONET_RUNTIME_STATE'
$LlmOwnershipMarker = ''
if ($StateFile) {
	$LlmOwnershipMarker = $StateFile + '.llm-owned'
}
$Root = [IO.Path]::GetFullPath((Env 'GOBBONET_ROOT' (Split-Path -Parent $MyInvocation.MyCommand.Path))).TrimEnd('\')
$RootPrefix = $Root + '\'
$LlmPort = [int](Env 'GOBBONET_LLM_PORT' '11437')
$EmbedPort = [int](Env 'GOBBONET_EMBED_PORT' '11436')
$SearchPort = [int](Env 'GOBBONET_SEARCH_PORT' '11435')
$WebPort = [int](Env 'GOBBONET_WEB_PORT' '9066')
$LogFile = Join-Path $Root 'runtime-watchdog.log'

function Log([string]$Text) {
	try {
		Add-Content -LiteralPath $LogFile `
			-Value ('[{0:yyyy-MM-dd HH:mm:ss}] {1}' -f (Get-Date), $Text) `
			-Encoding UTF8
	}
	catch {
		Write-Debug ("Could not append to runtime-watchdog.log: {0}" -f $_.Exception.Message)
	}
}

function Test-ParentAlive {
	if ($ParentPid -le 0) { return $false }
	return $null -ne (Get-Process -Id $ParentPid -ErrorAction SilentlyContinue)
}

function Read-Ownership {
	$o = @{ LLM = $false; EMBED = $false; SEARCH = $false; WEB = $false }

	if ($StateFile -and (Test-Path -LiteralPath $StateFile)) {
		foreach ($line in (Get-Content -LiteralPath $StateFile -ErrorAction SilentlyContinue)) {
			if ($line -match '^OWN_(LLM|EMBED|SEARCH|WEB)=(1|0)$') {
				$o[$Matches[1]] = ($Matches[2] -eq '1')
			}
		}
	}

	# Hot-swaps can promote LLM ownership after launch.bat wrote the shared
	# state. The marker is monotonic and has a single writer, so no cross-process
	# read-modify-write is required.
	if ($LlmOwnershipMarker -and (Test-Path -LiteralPath $LlmOwnershipMarker)) {
		$o.LLM = $true
	}

	return $o
}

function Test-PortArgument([string]$Cmd, [int]$Port) {
	if ([string]::IsNullOrWhiteSpace($Cmd)) { return $false }

	return $Cmd -match (
		'(?i)(?:--port\s+|--port=)' +
		[regex]::Escape([string]$Port) +
		'(?:\s|$)'
	)
}

function Test-CommandInRoot([string]$Cmd) {
	if ([string]::IsNullOrWhiteSpace($Cmd)) { return $false }
	return $Cmd.IndexOf($RootPrefix, [StringComparison]::OrdinalIgnoreCase) -ge 0
}

function Get-EncodedCommandText([string]$Cmd) {
	if ([string]::IsNullOrWhiteSpace($Cmd)) { return $null }

	# Windows PowerShell -EncodedCommand is UTF-16LE base64.
	if ($Cmd -notmatch '(?i)(?:^|\s)-(?:EncodedCommand|enc)\s+"?([A-Za-z0-9+/=]+)"?(?:\s|$)') {
		return $null
	}

	try {
		$bytes = [Convert]::FromBase64String($Matches[1])
		return [Text.Encoding]::Unicode.GetString($bytes)
	}
	catch {
		return $null
	}
}

function Test-GobboSearchProxy([string]$Cmd) {
	$decoded = Get-EncodedCommandText $Cmd
	if ([string]::IsNullOrWhiteSpace($decoded)) { return $false }

	# The 1.5.8 proxy is launched as PowerShell -EncodedCommand, so its normal
	# process command line does not contain the GobboNet project path. Identify
	# the script by several independent markers instead of relying on SEARCH_PORT:
	# 1.5.8 still hardcodes 11435 inside the encoded script even when
	# GEMMA_SEARCH_PORT is overridden by launch.bat.
	return (
		$decoded.IndexOf('System.Net.HttpListener', [StringComparison]::OrdinalIgnoreCase) -ge 0 -and
		$decoded -match 'http://127\.0\.0\.1:\d+/' -and
		$decoded.IndexOf('ollama.com/api', [StringComparison]::OrdinalIgnoreCase) -ge 0 -and
		$decoded.IndexOf('Access-Control-Allow-Origin', [StringComparison]::OrdinalIgnoreCase) -ge 0
	)
}

if ($ParentPid -le 0) { exit 0 }

# Snapshot processes before GobboNet starts its detached services. Ownership
# flags are the primary guard; this baseline is defense-in-depth against a
# failed spawn leaving OWN_* set to 1, and against accidentally matching a
# compatible process that was already running before this launcher instance.
$BaselineProcesses = @{}
foreach ($p in @(Get-CimInstance Win32_Process)) {
	$BaselineProcesses[[int]$p.ProcessId] = [string]$p.CreationDate
}

# A live Windows box always has processes -- this PowerShell among them --
# so an empty baseline means the CIM query failed, not that nothing was
# running. $ErrorActionPreference = 'SilentlyContinue' lets that failure
# pass unnoticed, and Test-PreexistingProcess then answers $false for every
# process: the guard that protects pre-existing services would protect
# nothing, exactly when we can no longer tell what was here first. Decline
# to clean up rather than clean up blind; leaked services are recoverable,
# a wrongly killed Ollama is not.
if ($BaselineProcesses.Count -eq 0) {
	Log 'baseline process query returned nothing -- automatic cleanup disabled for this launch'
	exit 1
}

function Test-PreexistingProcess($Process) {
	$pidValue = [int]$Process.ProcessId
	if (-not $BaselineProcesses.ContainsKey($pidValue)) { return $false }
	return $BaselineProcesses[$pidValue] -eq [string]$Process.CreationDate
}

Log ("watching launcher pid={0}; llm={1} embed={2} search={3} web={4}; baseline={5}" -f `
		$ParentPid, $LlmPort, $EmbedPort, $SearchPort, $WebPort, $BaselineProcesses.Count)

while (Test-ParentAlive) {
	Start-Sleep -Seconds 2
}

$own = Read-Ownership
Log ("launcher exited; ownership llm={0} embed={1} search={2} web={3}" -f `
		$own.LLM, $own.EMBED, $own.SEARCH, $own.WEB)

# Query command lines once. Cleanup is based on ownership + service identity,
# never on a port alone, so unrelated apps (especially Ollama) are left alone.
$procs = @(Get-CimInstance Win32_Process)

foreach ($p in $procs) {
	# Never touch a process that already existed when this watchdog started,
	# even if later ownership flags or identity matching would otherwise select it.
	if (Test-PreexistingProcess $p) { continue }

	$name = ([string]$p.Name).ToLowerInvariant()
	$cmd = [string]$p.CommandLine
	$kill = $false

	if ($name -eq 'llama-server.exe') {
		if (Test-CommandInRoot $cmd) {
			if ($own.LLM -and (Test-PortArgument $cmd $LlmPort)) { $kill = $true }
			if ($own.EMBED -and (Test-PortArgument $cmd $EmbedPort)) { $kill = $true }
		}
	}
	elseif ($name -in @('powershell.exe', 'pwsh.exe')) {
		# fileserver.ps1 has the project-root path in its command line.
		if ($own.WEB -and (Test-CommandInRoot $cmd) -and $cmd -match '(?i)fileserver\.ps1') {
			$kill = $true
		}

		# The 1.5.8 search proxy is an encoded PowerShell command and therefore
		# has no project-root path in its process command line.
		if ($own.SEARCH -and (Test-GobboSearchProxy $cmd)) {
			$kill = $true
		}
	}
	elseif ($name -eq 'cmd.exe') {
		if (Test-CommandInRoot $cmd) {
			if ($own.LLM -and $cmd -match '(?i)\.llama-launch\.cmd') { $kill = $true }
			if ($own.EMBED -and $cmd -match '(?i)\.embed-launch\.cmd') { $kill = $true }
		}
	}

	if ($kill) {
		try {
			Log ("stopping pid={0} name={1}" -f $p.ProcessId, $p.Name)
			Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
		}
		catch {
			Write-Debug ("Could not stop PID {0}: {1}" -f $p.ProcessId, $_.Exception.Message)
		}
	}
}

if ($StateFile) {
	Remove-Item -LiteralPath $StateFile -Force -ErrorAction SilentlyContinue
}
if ($LlmOwnershipMarker) {
	Remove-Item -LiteralPath $LlmOwnershipMarker -Force -ErrorAction SilentlyContinue
}

Log 'cleanup complete'
