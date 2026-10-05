$ErrorActionPreference = "Stop"
$Prefix = "__FAIRY_CUA_RESULT__"

function Write-Result {
  param([hashtable]$Payload)
  if (-not $Payload.ContainsKey("ok")) { $Payload["ok"] = $true }
  $Payload["platform"] = "windows"
  $json = $Payload | ConvertTo-Json -Compress -Depth 12
  [Console]::Out.WriteLine($Prefix + $json)
}

function Fail {
  param([string]$Message)
  Write-Result @{ ok = $false; code = "execution_error"; error = $Message }
  exit 0
}

Add-Type -AssemblyName System.Windows.Forms | Out-Null
Add-Type -AssemblyName System.Drawing | Out-Null
Add-Type -TypeDefinition @"
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

public static class FairyWin {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X; public int Y; }
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

  [DllImport("user32.dll")] public static extern bool GetCursorPos(out POINT p);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extraInfo);

  [StructLayout(LayoutKind.Sequential)]
  public struct MOUSEINPUT {
    public int dx;
    public int dy;
    public uint mouseData;
    public uint dwFlags;
    public uint time;
    public UIntPtr dwExtraInfo;
  }

  [StructLayout(LayoutKind.Sequential)]
  public struct INPUT {
    public uint type;
    public MOUSEINPUT mi;
  }

  [DllImport("user32.dll", SetLastError = true)]
  public static extern uint SendInput(uint nInputs, INPUT[] pInputs, int cbSize);

  [DllImport("user32.dll")] public static extern int GetSystemMetrics(int nIndex);
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value);

  // Absolute moves must be normalised across the whole virtual desktop
  // (MOUSEEVENTF_VIRTUALDESK). Without it SendInput clamps to the primary
  // monitor and the pointer lands somewhere else entirely.
  public static uint SendMouse(uint flags, int x, int y, bool absolute) {
    int vx = GetSystemMetrics(76);
    int vy = GetSystemMetrics(77);
    int vw = GetSystemMetrics(78);
    int vh = GetSystemMetrics(79);
    int dx = x;
    int dy = y;
    if (absolute) {
      flags |= 0x8000 | 0x4000;
      dx = vw > 1 ? (int)((long)(x - vx) * 65535 / (vw - 1)) : 0;
      dy = vh > 1 ? (int)((long)(y - vy) * 65535 / (vh - 1)) : 0;
    }
    INPUT[] inputs = new INPUT[1];
    inputs[0].type = 0;
    inputs[0].mi = new MOUSEINPUT {
      dx = dx,
      dy = dy,
      mouseData = 0,
      dwFlags = flags,
      time = 0,
      dwExtraInfo = UIntPtr.Zero
    };
    return SendInput(1, inputs, Marshal.SizeOf(typeof(INPUT)));
  }
  [DllImport("user32.dll")] public static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extraInfo);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int maxCount);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowTextLength(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int command);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr hWnd, int x, int y, int width, int height, bool repaint);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hWnd, uint message, IntPtr wParam, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

  public static string WindowTitle(IntPtr hWnd) {
    int length = GetWindowTextLength(hWnd);
    if (length <= 0) return "";
    var buffer = new StringBuilder(length + 1);
    GetWindowText(hWnd, buffer, buffer.Capacity);
    return buffer.ToString();
  }

  public static int[] WindowRect(IntPtr hWnd) {
    RECT rect;
    if (!GetWindowRect(hWnd, out rect)) return new int[] { 0, 0, 0, 0 };
    return new int[] { rect.Left, rect.Top, rect.Right - rect.Left, rect.Bottom - rect.Top };
  }

  public static string[] ListWindows() {
    var windows = new List<string>();
    EnumWindows(delegate(IntPtr hWnd, IntPtr lParam) {
      if (!IsWindowVisible(hWnd)) return true;
      string title = WindowTitle(hWnd);
      if (String.IsNullOrWhiteSpace(title)) return true;
      int[] rect = WindowRect(hWnd);
      windows.Add(hWnd.ToInt64() + "\t" + title.Replace("\t", " ") + "\t" + rect[0] + "\t" + rect[1] + "\t" + rect[2] + "\t" + rect[3]);
      return true;
    }, IntPtr.Zero);
    return windows.ToArray();
  }
}
"@ | Out-Null
try { [void][FairyWin]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) }
catch { try { [void][FairyWin]::SetProcessDPIAware() } catch { } }

$rawInput = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($rawInput)) { Fail "request is empty" }
try { $Request = $rawInput | ConvertFrom-Json } catch { Fail "invalid JSON request: $($_.Exception.Message)" }

function Require-Int($Value, $Field) {
  if ($null -eq $Value -or "$Value" -eq "") { throw "$Field is required" }
  return [int]$Value
}

function Optional-Int($Value) {
  if ($null -eq $Value -or "$Value" -eq "") { return $null }
  return [int]$Value
}

function Mouse-Flags([string]$Button, [bool]$Down) {
  switch ($Button.ToLowerInvariant()) {
    "left"   { if ($Down) { return 0x0002 } else { return 0x0004 } }
    "right"  { if ($Down) { return 0x0008 } else { return 0x0010 } }
    "middle" { if ($Down) { return 0x0020 } else { return 0x0040 } }
    default  { throw "unsupported mouse button: $Button" }
  }
}

function Move-Cursor($X, $Y) {
  # Same contract as the C# broker: absolute physical pixels, then read the
  # cursor back instead of assuming the move worked.
  [void][FairyWin]::SendMouse(0x0001, [int]$X, [int]$Y, $true)
  Start-Sleep -Milliseconds 25
  $point = New-Object FairyWin+POINT
  [void][FairyWin]::GetCursorPos([ref]$point)
  if ([Math]::Abs($point.X - [int]$X) -gt 1 -or [Math]::Abs($point.Y - [int]$Y) -gt 1) {
    [void][FairyWin]::SetCursorPos([int]$X, [int]$Y)
    Start-Sleep -Milliseconds 25
  }
}

function Mouse-Down([string]$Button) { [void][FairyWin]::SendMouse((Mouse-Flags $Button $true), 0, 0, $false) }
function Mouse-Up([string]$Button) { [void][FairyWin]::SendMouse((Mouse-Flags $Button $false), 0, 0, $false) }

function Click-At($X, $Y, [string]$Button) {
  Move-Cursor $X $Y
  Mouse-Down $Button
  Start-Sleep -Milliseconds 60
  Mouse-Up $Button
}

function Resolve-VirtualKey([string]$Key) {
  $name = $Key.Trim()
  if ($name -match "^[A-Za-z]$") { return [int][char]::ToUpperInvariant($name[0]) }
  if ($name -match "^[0-9]$") { return [int][char]$name[0] }
  $map = @{
    "ctrl"="ControlKey"; "control"="ControlKey"; "shift"="ShiftKey"; "alt"="Menu"; "win"="LWin"; "meta"="LWin";
    "enter"="Enter"; "return"="Enter"; "esc"="Escape"; "escape"="Escape"; "space"="Space"; "tab"="Tab";
    "backspace"="Back"; "delete"="Delete"; "insert"="Insert"; "home"="Home"; "end"="End";
    "pageup"="PageUp"; "pagedown"="PageDown"; "up"="Up"; "down"="Down"; "left"="Left"; "right"="Right";
    "capslock"="CapsLock"
  }
  if ($map.ContainsKey($name.ToLowerInvariant())) { $name = $map[$name.ToLowerInvariant()] }
  return [int][Enum]::Parse([System.Windows.Forms.Keys], $name, $true)
}

function Key-Down([string]$Key) {
  $vk = Resolve-VirtualKey $Key
  [FairyWin]::keybd_event([byte]$vk, 0, 0, [UIntPtr]::Zero)
}

function Key-Up([string]$Key) {
  $vk = Resolve-VirtualKey $Key
  [FairyWin]::keybd_event([byte]$vk, 0, 2, [UIntPtr]::Zero)
}

function Send-Text([string]$Text) {
  Add-Type -AssemblyName System.Windows.Forms | Out-Null
  $old = [System.Windows.Forms.Clipboard]::GetText()
  [System.Windows.Forms.Clipboard]::SetText($Text)
  [System.Windows.Forms.SendKeys]::SendWait("^v")
  Start-Sleep -Milliseconds 160
  if ([string]::IsNullOrEmpty($old)) { [System.Windows.Forms.Clipboard]::Clear() } else { [System.Windows.Forms.Clipboard]::SetText($old) }
}

function Handle([string]$Value) {
  $parsed = 0L
  if (-not [Int64]::TryParse($Value, [ref]$parsed)) { throw "invalid window handle: $Value" }
  return [IntPtr]$parsed
}

function Windows-Observe($Request) {
  switch ($Request.action) {
    "screen_info" {
      $screen = [System.Windows.Forms.SystemInformation]::VirtualScreen
      $point = New-Object FairyWin+POINT
      [void][FairyWin]::GetCursorPos([ref]$point)
      return @{ width = [int]$screen.Width; height = [int]$screen.Height; cursor = @{ x = $point.X; y = $point.Y }; display_scale = 1.0 }
    }
    "cursor_position" {
      $point = New-Object FairyWin+POINT
      [void][FairyWin]::GetCursorPos([ref]$point)
      return @{ x = $point.X; y = $point.Y }
    }
    "screenshot" {
      $path = [string]$Request.output_path
      if ([string]::IsNullOrWhiteSpace($path)) { throw "output_path is required" }
      $full = [System.IO.Path]::GetFullPath($path)
      [System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($full)) | Out-Null
      $screen = [System.Windows.Forms.SystemInformation]::VirtualScreen
      $bitmap = New-Object System.Drawing.Bitmap([int]$screen.Width, [int]$screen.Height)
      $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
      try { $graphics.CopyFromScreen($screen.Left, $screen.Top, 0, 0, $bitmap.Size) } finally { $graphics.Dispose() }
      try { $bitmap.Save($full, [System.Drawing.Imaging.ImageFormat]::Png) } finally { $bitmap.Dispose() }
      return @{ path = $full; width = [int]$screen.Width; height = [int]$screen.Height; size = (Get-Item -LiteralPath $full).Length }
    }
    "active_window" {
      $h = [FairyWin]::GetForegroundWindow()
      if ($h -eq [IntPtr]::Zero) { return @{ title = ""; handle = "" } }
      $rect = [FairyWin]::WindowRect($h)
      return @{ title = [FairyWin]::WindowTitle($h); handle = [string]$h.ToInt64(); x = $rect[0]; y = $rect[1]; width = $rect[2]; height = $rect[3] }
    }
    "list_windows" {
      $filter = [string]$Request.title
      $items = @()
      foreach ($line in [FairyWin]::ListWindows()) {
        $parts = $line -split "`t", 6
        if ($parts.Count -lt 6) { continue }
        if ($filter -and $parts[1].ToLowerInvariant() -notlike "*$($filter.ToLowerInvariant())*") { continue }
        $items += @{ handle = $parts[0]; title = $parts[1]; x = [int]$parts[2]; y = [int]$parts[3]; width = [int]$parts[4]; height = [int]$parts[5] }
      }
      return @{ windows = $items; count = $items.Count }
    }
    default { throw "unsupported observe action: $($Request.action)" }
  }
}

function Windows-Pointer($Request) {
  $button = if ($Request.button) { [string]$Request.button } else { "left" }
  switch ($Request.action) {
    "move" { Move-Cursor (Require-Int $Request.x "x") (Require-Int $Request.y "y") }
    "click" { Click-At (Require-Int $Request.x "x") (Require-Int $Request.y "y") $button }
    "double_click" { Click-At (Require-Int $Request.x "x") (Require-Int $Request.y "y") "left"; Start-Sleep -Milliseconds 80; Click-At (Require-Int $Request.x "x") (Require-Int $Request.y "y") "left" }
    "right_click" { Click-At (Require-Int $Request.x "x") (Require-Int $Request.y "y") "right" }
    "drag" {
      Move-Cursor (Require-Int $Request.start_x "start_x") (Require-Int $Request.start_y "start_y")
      Mouse-Down $button
      Move-Cursor (Require-Int $Request.end_x "end_x") (Require-Int $Request.end_y "end_y")
      Mouse-Up $button
    }
    "scroll" {
      $dy = if ($null -eq $Request.dy) { 0 } else { [int]$Request.dy }
      $dx = if ($null -eq $Request.dx) { 0 } else { [int]$Request.dx }
      if ($dy -ne 0) { [FairyWin]::mouse_event(0x0800, 0, 0, [uint32]($dy * 120), [UIntPtr]::Zero) }
      if ($dx -ne 0) { [FairyWin]::mouse_event(0x1000, 0, 0, [uint32]($dx * 120), [UIntPtr]::Zero) }
    }
    "mouse_down" {
      $x = Optional-Int $Request.x; $y = Optional-Int $Request.y
      if ($null -ne $x -and $null -ne $y) { Move-Cursor $x $y }
      Mouse-Down $button
    }
    "mouse_up" {
      $x = Optional-Int $Request.x; $y = Optional-Int $Request.y
      if ($null -ne $x -and $null -ne $y) { Move-Cursor $x $y }
      Mouse-Up $button
    }
    default { throw "unsupported pointer action: $($Request.action)" }
  }
  return @{ action = $Request.action; button = $button }
}

function Windows-Keyboard($Request) {
  switch ($Request.action) {
    "type" { Send-Text ([string]$Request.text) }
    "press" { $key = [string]$Request.key; Key-Down $key; Key-Up $key }
    "hotkey" {
      $keys = @($Request.keys | ForEach-Object { [string]$_ })
      if ($keys.Count -eq 0) { throw "keys must be a non-empty array" }
      foreach ($key in $keys) { Key-Down $key }
      [array]::Reverse($keys)
      foreach ($key in $keys) { Key-Up $key }
    }
    "key_down" { Key-Down ([string]$Request.key) }
    "key_up" { Key-Up ([string]$Request.key) }
    default { throw "unsupported keyboard action: $($Request.action)" }
  }
  return @{ action = $Request.action }
}

function Windows-Window($Request) {
  switch ($Request.action) {
    "open" {
      $target = [string]$Request.target
      if ([string]::IsNullOrWhiteSpace($target)) { throw "target is required" }
      Start-Process -FilePath $target
      return @{ changed = $true; target = $target }
    }
    "activate" { $h = Handle ([string]$Request.handle); [void][FairyWin]::ShowWindow($h, 9); return @{ changed = [FairyWin]::SetForegroundWindow($h); handle = [string]$h.ToInt64(); action = "activate" } }
    "minimize" { $h = Handle ([string]$Request.handle); return @{ changed = [FairyWin]::ShowWindow($h, 6); handle = [string]$h.ToInt64(); action = "minimize" } }
    "maximize" { $h = Handle ([string]$Request.handle); return @{ changed = [FairyWin]::ShowWindow($h, 3); handle = [string]$h.ToInt64(); action = "maximize" } }
    "close" { $h = Handle ([string]$Request.handle); return @{ changed = [FairyWin]::PostMessage($h, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero); handle = [string]$h.ToInt64(); action = "close" } }
    "move" { $h = Handle ([string]$Request.handle); $r = [FairyWin]::WindowRect($h); return @{ changed = [FairyWin]::MoveWindow($h, (Require-Int $Request.x "x"), (Require-Int $Request.y "y"), $r[2], $r[3], $true); handle = [string]$h.ToInt64(); action = "move" } }
    "resize" { $h = Handle ([string]$Request.handle); $r = [FairyWin]::WindowRect($h); return @{ changed = [FairyWin]::MoveWindow($h, $r[0], $r[1], (Require-Int $Request.width "width"), (Require-Int $Request.height "height"), $true); handle = [string]$h.ToInt64(); action = "resize" } }
    default { throw "unsupported window action: $($Request.action)" }
  }
}

function Windows-Clipboard($Request) {
  switch ($Request.action) {
    "get" { return @{ text = [System.Windows.Forms.Clipboard]::GetText() } }
    "set" { [System.Windows.Forms.Clipboard]::SetText([string]$Request.text); return @{ changed = $true } }
    default { throw "unsupported clipboard action: $($Request.action)" }
  }
}

try {
  switch ($Request.tool) {
    "computer_observe" { Write-Result (Windows-Observe $Request) }
    "computer_pointer" { Write-Result (Windows-Pointer $Request) }
    "computer_keyboard" { Write-Result (Windows-Keyboard $Request) }
    "computer_window" { Write-Result (Windows-Window $Request) }
    "computer_clipboard" { Write-Result (Windows-Clipboard $Request) }
    default { Fail "unsupported computer tool: $($Request.tool)" }
  }
} catch {
  Fail "$($_.Exception.GetType().Name): $($_.Exception.Message)"
}
