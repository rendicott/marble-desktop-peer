# marble-peer Windows notification-area icon. Spawned by `marble-peer run`.
#
# Env:
#   MARBLE_PEER_PID         parent Go process; the icon exits when it is gone
#   MARBLE_PEER_STATUS_URL  http://127.0.0.1:PORT/status.json
#   MARBLE_PEER_MINIUI      http://127.0.0.1:PORT
#
# Keep this file ASCII-only: Windows PowerShell 5.1 reads BOM-less files as ANSI.
# Polls status.json every 2s from one WinForms timer (never a tight loop; see
# docs/idle-cpu-status-storm.md).

$ErrorActionPreference = 'Continue'

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$parentPid = 0
[void][int]::TryParse([string]$env:MARBLE_PEER_PID, [ref]$parentPid)
$statusUrl = [string]$env:MARBLE_PEER_STATUS_URL
$miniUI = [string]$env:MARBLE_PEER_MINIUI
$script:lastConfirmCount = 0

function Get-PeerStatus {
    if (-not $statusUrl) { return $null }
    try {
        return Invoke-RestMethod -Uri $statusUrl -TimeoutSec 2 -Headers @{ Accept = 'application/json' }
    } catch {
        return $null
    }
}

function Get-BaseUrl($st) {
    if ($st -and $st.miniui_addr) { return [string]$st.miniui_addr }
    return $miniUI
}

function Invoke-PeerPost([string]$path) {
    $base = Get-BaseUrl (Get-PeerStatus)
    if (-not $base) { return $false }
    try {
        Invoke-WebRequest -Uri ($base.TrimEnd('/') + $path) -Method Post -Body '' -UseBasicParsing -TimeoutSec 3 | Out-Null
        return $true
    } catch {
        return $false
    }
}

function New-DotIcon([System.Drawing.Color]$color) {
    $bmp = New-Object System.Drawing.Bitmap 16, 16
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $g.Clear([System.Drawing.Color]::Transparent)
    $brush = New-Object System.Drawing.SolidBrush $color
    $g.FillEllipse($brush, 2, 2, 12, 12)
    $brush.Dispose()
    $g.Dispose()
    $icon = [System.Drawing.Icon]::FromHandle($bmp.GetHicon())
    $bmp.Dispose()
    return $icon
}

$iconOnline = New-DotIcon ([System.Drawing.Color]::SeaGreen)
$iconIdle = New-DotIcon ([System.Drawing.Color]::Gray)
$iconAlert = New-DotIcon ([System.Drawing.Color]::DarkOrange)

$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Icon = $iconIdle
$notify.Text = 'Marble Peer'

$menu = New-Object System.Windows.Forms.ContextMenuStrip

$itemStatus = $menu.Items.Add('Status: starting...')
$itemStatus.Enabled = $false
$itemId = $menu.Items.Add('Computer: -')
$itemId.Enabled = $false
[void]$menu.Items.Add('-')
$itemConfirm = $menu.Items.Add('No pending confirmations')
$itemConfirm.Enabled = $false
$itemOpen = $menu.Items.Add('Open mini UI')
$itemStop = $menu.Items.Add('Stop current action')
[void]$menu.Items.Add('-')
$itemQuit = $menu.Items.Add('Quit marble-peer')

$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 2000

function Stop-Tray {
    $timer.Stop()
    $notify.Visible = $false
    $notify.Dispose()
    [System.Windows.Forms.Application]::Exit()
}

function Open-Url([string]$url) {
    if ($url) { Start-Process $url }
}

function Open-Confirm {
    $st = Get-PeerStatus
    $pending = @()
    if ($st -and $st.pending_confirms) { $pending = @($st.pending_confirms) }
    if ($pending.Count -gt 0 -and $pending[0].url) {
        Open-Url ([string]$pending[0].url)
        return
    }
    Open-Url (Get-BaseUrl $st)
}

function Update-Tray {
    if ($parentPid -gt 0 -and -not (Get-Process -Id $parentPid -ErrorAction SilentlyContinue)) {
        Stop-Tray
        return
    }
    $st = Get-PeerStatus
    if (-not $st) {
        $itemStatus.Text = 'Status: peer unreachable'
        $notify.Icon = $iconIdle
        $notify.Text = 'Marble Peer - unreachable'
        return
    }
    $state = 'unknown'
    if ($st.state) { $state = [string]$st.state }
    $cid = '-'
    if ($st.computer_id) { $cid = [string]$st.computer_id }
    $browser = 'no-browser'
    if ($st.browser_ready) { $browser = 'browser' }
    $pending = @()
    if ($st.pending_confirms) { $pending = @($st.pending_confirms) }
    $n = $pending.Count

    $itemStatus.Text = "Status: $state ($browser)"
    $itemId.Text = "Computer: $cid"
    if ($n -gt 0) {
        $itemConfirm.Text = "(!) Confirm action ($n) - click"
        $itemConfirm.Enabled = $true
        $tip = "Marble Peer - CONFIRM needed ($n)"
        $notify.Icon = $iconAlert
        if ($n -gt $script:lastConfirmCount) {
            $notify.ShowBalloonTip(10000, 'Marble Peer', 'An action is waiting for your confirmation.', [System.Windows.Forms.ToolTipIcon]::Warning)
        }
    } else {
        $itemConfirm.Text = 'No pending confirmations'
        $itemConfirm.Enabled = $false
        $tip = "Marble Peer - $state"
        if ($cid -ne '-') { $tip = "$tip ($cid)" }
        if ($state -eq 'online') { $notify.Icon = $iconOnline } else { $notify.Icon = $iconIdle }
    }
    $script:lastConfirmCount = $n
    # NotifyIcon.Text throws when longer than 63 characters.
    if ($tip.Length -gt 63) { $tip = $tip.Substring(0, 63) }
    $notify.Text = $tip
}

$itemConfirm.add_Click({ Open-Confirm })
$itemOpen.add_Click({ Open-Url (Get-BaseUrl (Get-PeerStatus)) })
$itemStop.add_Click({ [void](Invoke-PeerPost '/stop') })
$itemQuit.add_Click({
    # Prefer the HTTP quit so the daemon flushes logs and exits 0.
    if (-not (Invoke-PeerPost '/quit') -and $parentPid -gt 0) {
        Stop-Process -Id $parentPid -Force -ErrorAction SilentlyContinue
    }
    Stop-Tray
})
$notify.add_DoubleClick({ Open-Confirm })

$timer.add_Tick({
    try { Update-Tray } catch { [Console]::Error.WriteLine("tray refresh: $_") }
})

$notify.ContextMenuStrip = $menu
$notify.Visible = $true
try { Update-Tray } catch { [Console]::Error.WriteLine("tray refresh: $_") }
$timer.Start()
try {
    [System.Windows.Forms.Application]::Run()
} finally {
    $notify.Visible = $false
    $notify.Dispose()
}
