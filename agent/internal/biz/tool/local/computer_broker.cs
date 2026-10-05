// Fairy Computer Broker
//
// Windows desktop automation broker used by the Fairy agent's computer_* tools.
//
// Why this exists instead of an ad-hoc PowerShell script:
//   1. The process becomes Per-Monitor-V2 DPI aware before any Win32/UI call, so
//      screenshot pixels, window rectangles and cursor positions all live in the
//      same physical-pixel space.
//   2. Every action reports which coordinate space it used plus the current
//      geometry hash. Callers can pass `expected_geometry_hash` back so a stale
//      coordinate fails loudly instead of silently clicking the wrong place.
//   3. Pointer movement is verified: after SendInput the cursor is read back and
//      compared with the requested point, with SetCursorPos as a fallback.
//   4. UIA (UI Automation) is the preferred way to reach a control; raw input is
//      the fallback. Protected processes may still reject synthetic input - that
//      is a platform boundary this tool does not try to defeat.
//
// Protocol: one JSON object on stdin, one `__FAIRY_CUA_RESULT__` prefixed JSON
// line on stdout. Mirrors the legacy PowerShell bridge so both stay swappable.

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Imaging;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Web.Script.Serialization;
using System.Windows.Automation;
using System.Windows.Forms;

internal static class FairyComputerBroker
{
    internal const string ResultPrefix = "__FAIRY_CUA_RESULT__";
    internal const string BridgeVersion = "csharp-broker/1.0";

    private static readonly JavaScriptSerializer Json = new JavaScriptSerializer();

    // ---------------------------------------------------------------------
    // Win32 interop
    // ---------------------------------------------------------------------

    [StructLayout(LayoutKind.Sequential)]
    internal struct RECT
    {
        public int Left;
        public int Top;
        public int Right;
        public int Bottom;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct POINT
    {
        public int X;
        public int Y;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct MOUSEINPUT
    {
        public int dx;
        public int dy;
        public uint mouseData;
        public uint dwFlags;
        public uint time;
        public IntPtr dwExtraInfo;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct KEYBDINPUT
    {
        public ushort wVk;
        public ushort wScan;
        public uint dwFlags;
        public uint time;
        public IntPtr dwExtraInfo;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct HARDWAREINPUT
    {
        public uint uMsg;
        public ushort wParamL;
        public ushort wParamH;
    }

    [StructLayout(LayoutKind.Explicit)]
    internal struct InputUnion
    {
        [FieldOffset(0)]
        public MOUSEINPUT mi;
        [FieldOffset(0)]
        public KEYBDINPUT ki;
        [FieldOffset(0)]
        public HARDWAREINPUT hi;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct INPUT
    {
        public uint type;
        public InputUnion u;
    }

    internal delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
    internal delegate bool MonitorEnumProc(IntPtr hMonitor, IntPtr hdc, ref RECT rect, IntPtr data);

    private const int INPUT_MOUSE = 0;
    private const int INPUT_KEYBOARD = 1;

    private const uint MOUSEEVENTF_MOVE = 0x0001;
    private const uint MOUSEEVENTF_LEFTDOWN = 0x0002;
    private const uint MOUSEEVENTF_LEFTUP = 0x0004;
    private const uint MOUSEEVENTF_RIGHTDOWN = 0x0008;
    private const uint MOUSEEVENTF_RIGHTUP = 0x0010;
    private const uint MOUSEEVENTF_MIDDLEDOWN = 0x0020;
    private const uint MOUSEEVENTF_MIDDLEUP = 0x0040;
    private const uint MOUSEEVENTF_WHEEL = 0x0800;
    private const uint MOUSEEVENTF_HWHEEL = 0x1000;
    private const uint MOUSEEVENTF_ABSOLUTE = 0x8000;
    private const uint MOUSEEVENTF_VIRTUALDESK = 0x4000;

    private const uint KEYEVENTF_EXTENDEDKEY = 0x0001;
    private const uint KEYEVENTF_KEYUP = 0x0002;
    private const uint KEYEVENTF_UNICODE = 0x0004;
    private const uint KEYEVENTF_SCANCODE = 0x0008;

    private const int SM_XVIRTUALSCREEN = 76;
    private const int SM_YVIRTUALSCREEN = 77;
    private const int SM_CXVIRTUALSCREEN = 78;
    private const int SM_CYVIRTUALSCREEN = 79;

    private const int DWMWA_EXTENDED_FRAME_BOUNDS = 9;
    private const uint PW_RENDERFULLCONTENT = 0x00000002;

    [DllImport("user32.dll")]
    internal static extern int GetSystemMetrics(int nIndex);

    [DllImport("user32.dll")]
    internal static extern bool GetCursorPos(out POINT p);

    [DllImport("user32.dll")]
    internal static extern bool SetCursorPos(int x, int y);

    [DllImport("user32.dll", SetLastError = true)]
    internal static extern uint SendInput(uint nInputs, INPUT[] pInputs, int cbSize);

    [DllImport("user32.dll")]
    internal static extern bool SetProcessDPIAware();

    [DllImport("user32.dll")]
    internal static extern bool SetProcessDpiAwarenessContext(IntPtr value);

    [DllImport("shcore.dll")]
    internal static extern int SetProcessDpiAwareness(int value);

    [DllImport("shcore.dll")]
    internal static extern int GetDpiForMonitor(IntPtr hmonitor, int dpiType, out uint dpiX, out uint dpiY);

    [DllImport("user32.dll")]
    internal static extern IntPtr MonitorFromWindow(IntPtr hwnd, uint flags);

    [DllImport("user32.dll")]
    internal static extern bool EnumDisplayMonitors(IntPtr hdc, IntPtr lprcClip, MonitorEnumProc lpfnEnum, IntPtr dwData);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    internal static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int maxCount);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    internal static extern int GetWindowTextLength(IntPtr hWnd);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    internal static extern int GetClassName(IntPtr hWnd, StringBuilder text, int maxCount);

    [DllImport("user32.dll")]
    internal static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

    [DllImport("user32.dll")]
    internal static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    internal static extern bool IsIconic(IntPtr hWnd);

    [DllImport("user32.dll")]
    internal static extern bool IsWindow(IntPtr hWnd);

    [DllImport("user32.dll")]
    internal static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);

    [DllImport("user32.dll")]
    internal static extern bool GetClientRect(IntPtr hWnd, out RECT rect);

    [DllImport("user32.dll")]
    internal static extern bool ClientToScreen(IntPtr hWnd, ref POINT point);

    [DllImport("user32.dll")]
    internal static extern bool ScreenToClient(IntPtr hWnd, ref POINT point);

    [DllImport("user32.dll")]
    internal static extern bool ShowWindow(IntPtr hWnd, int command);

    [DllImport("user32.dll")]
    internal static extern bool SetForegroundWindow(IntPtr hWnd);

    [DllImport("user32.dll")]
    internal static extern bool BringWindowToTop(IntPtr hWnd);

    [DllImport("user32.dll")]
    internal static extern IntPtr GetForegroundWindow();

    [DllImport("user32.dll")]
    internal static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

    [DllImport("user32.dll")]
    internal static extern bool AttachThreadInput(uint idAttach, uint idAttachTo, bool fAttach);

    [DllImport("kernel32.dll")]
    internal static extern uint GetCurrentThreadId();

    [DllImport("user32.dll")]
    internal static extern bool MoveWindow(IntPtr hWnd, int x, int y, int width, int height, bool repaint);

    [DllImport("user32.dll")]
    internal static extern bool PostMessage(IntPtr hWnd, uint message, IntPtr wParam, IntPtr lParam);

    [DllImport("dwmapi.dll")]
    internal static extern int DwmGetWindowAttribute(IntPtr hwnd, int attribute, out RECT value, int size);

    [DllImport("gdi32.dll")]
    internal static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint nFlags);

    // ---------------------------------------------------------------------
    // DPI + geometry
    // ---------------------------------------------------------------------

    private static void EnableDpiAwareness()
    {
        // Per-Monitor-V2 first (Windows 10 1703+), then the older APIs.
        try
        {
            if (SetProcessDpiAwarenessContext(new IntPtr(-4)))
            {
                return;
            }
        }
        catch (Exception)
        {
        }
        try
        {
            if (SetProcessDpiAwareness(2) == 0)
            {
                return;
            }
        }
        catch (Exception)
        {
        }
        try
        {
            SetProcessDPIAware();
        }
        catch (Exception)
        {
        }
    }

    private struct VirtualDesktop
    {
        public int X;
        public int Y;
        public int Width;
        public int Height;
    }

    private static VirtualDesktop CurrentVirtualDesktop()
    {
        VirtualDesktop vd = new VirtualDesktop();
        vd.X = GetSystemMetrics(SM_XVIRTUALSCREEN);
        vd.Y = GetSystemMetrics(SM_YVIRTUALSCREEN);
        vd.Width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
        vd.Height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
        return vd;
    }

    private static string GeometryHash(VirtualDesktop vd)
    {
        return string.Format(CultureInfo.InvariantCulture, "geom:{0},{1},{2},{3}", vd.X, vd.Y, vd.Width, vd.Height);
    }

    private static int NormalizeAxis(int value, int origin, int size)
    {
        if (size <= 1)
        {
            return 0;
        }
        double scaled = (value - origin) * 65535.0 / (size - 1);
        return (int)Math.Round(scaled, MidpointRounding.AwayFromZero);
    }

    private static bool GeometryMatches(Dictionary<string, object> request, Dictionary<string, object> error)
    {
        string expected = Str(request, "expected_geometry_hash");
        if (string.IsNullOrEmpty(expected))
        {
            return true;
        }
        string current = GeometryHash(CurrentVirtualDesktop());
        if (string.Equals(expected, current, StringComparison.Ordinal))
        {
            return true;
        }
        error["ok"] = false;
        error["code"] = "stale_coordinate";
        error["error"] = "screen geometry changed since the coordinates were captured";
        error["expected_geometry_hash"] = expected;
        error["current_geometry_hash"] = current;
        return false;
    }

    // ---------------------------------------------------------------------
    // JSON helpers
    // ---------------------------------------------------------------------

    private static string Str(Dictionary<string, object> source, string key)
    {
        object value;
        if (source == null || !source.TryGetValue(key, out value) || value == null)
        {
            return null;
        }
        if (value is string)
        {
            return (string)value;
        }
        return Convert.ToString(value, CultureInfo.InvariantCulture);
    }

    private static int IntValue(Dictionary<string, object> source, string key, int fallback)
    {
        object value;
        if (source == null || !source.TryGetValue(key, out value) || value == null)
        {
            return fallback;
        }
        int parsed;
        if (int.TryParse(Convert.ToString(value, CultureInfo.InvariantCulture), NumberStyles.Integer, CultureInfo.InvariantCulture, out parsed))
        {
            return parsed;
        }
        return fallback;
    }

    private static bool BoolValue(Dictionary<string, object> source, string key, bool fallback)
    {
        object value;
        if (source == null || !source.TryGetValue(key, out value) || value == null)
        {
            return fallback;
        }
        if (value is bool)
        {
            return (bool)value;
        }
        bool parsed;
        if (bool.TryParse(Convert.ToString(value, CultureInfo.InvariantCulture), out parsed))
        {
            return parsed;
        }
        return fallback;
    }

    private static int RequiredInt(Dictionary<string, object> source, string key)
    {
        object value;
        if (source == null || !source.TryGetValue(key, out value) || value == null)
        {
            throw new ArgumentException(key + " is required");
        }
        int parsed;
        if (!int.TryParse(Convert.ToString(value, CultureInfo.InvariantCulture), NumberStyles.Integer, CultureInfo.InvariantCulture, out parsed))
        {
            throw new ArgumentException(key + " must be an integer");
        }
        return parsed;
    }

    private static IntPtr HandleValue(Dictionary<string, object> source, string key, bool required)
    {
        string text = Str(source, key);
        if (string.IsNullOrEmpty(text))
        {
            if (required)
            {
                throw new ArgumentException(key + " is required");
            }
            return IntPtr.Zero;
        }
        long parsed;
        if (!long.TryParse(text, NumberStyles.Integer, CultureInfo.InvariantCulture, out parsed))
        {
            throw new ArgumentException("invalid window handle: " + text);
        }
        return new IntPtr(parsed);
    }

    private static Dictionary<string, object> PointObject(int x, int y)
    {
        Dictionary<string, object> point = new Dictionary<string, object>();
        point["x"] = x;
        point["y"] = y;
        return point;
    }

    private static Dictionary<string, object> RectObject(int x, int y, int width, int height)
    {
        Dictionary<string, object> rect = new Dictionary<string, object>();
        rect["x"] = x;
        rect["y"] = y;
        rect["width"] = width;
        rect["height"] = height;
        return rect;
    }

    private static POINT CursorPosition()
    {
        POINT point;
        GetCursorPos(out point);
        return point;
    }

    private static List<Dictionary<string, object>> EnumerateMonitors()
    {
        List<Dictionary<string, object>> monitors = new List<Dictionary<string, object>>();
        MonitorEnumProc callback = delegate(IntPtr hMonitor, IntPtr hdc, ref RECT rect, IntPtr data)
        {
            Dictionary<string, object> item = new Dictionary<string, object>();
            item["x"] = rect.Left;
            item["y"] = rect.Top;
            item["width"] = rect.Right - rect.Left;
            item["height"] = rect.Bottom - rect.Top;
            item["primary"] = rect.Left == 0 && rect.Top == 0;
            uint dpiX = 96;
            uint dpiY = 96;
            try
            {
                GetDpiForMonitor(hMonitor, 0, out dpiX, out dpiY);
            }
            catch (Exception)
            {
            }
            item["dpi"] = (int)dpiX;
            item["scale"] = Math.Round(dpiX / 96.0, 4);
            monitors.Add(item);
            return true;
        };
        EnumDisplayMonitors(IntPtr.Zero, IntPtr.Zero, callback, IntPtr.Zero);
        return monitors;
    }

    // ---------------------------------------------------------------------
    // Window helpers
    // ---------------------------------------------------------------------

    private static string WindowTitle(IntPtr hwnd)
    {
        int length = GetWindowTextLength(hwnd);
        if (length <= 0)
        {
            return string.Empty;
        }
        StringBuilder buffer = new StringBuilder(length + 1);
        GetWindowText(hwnd, buffer, buffer.Capacity);
        return buffer.ToString();
    }

    private static string WindowClass(IntPtr hwnd)
    {
        StringBuilder buffer = new StringBuilder(256);
        GetClassName(hwnd, buffer, buffer.Capacity);
        return buffer.ToString();
    }

    private static int WindowProcessId(IntPtr hwnd)
    {
        uint pid;
        GetWindowThreadProcessId(hwnd, out pid);
        return (int)pid;
    }

    private static RECT RawWindowRect(IntPtr hwnd)
    {
        RECT rect;
        if (!GetWindowRect(hwnd, out rect))
        {
            return new RECT();
        }
        return rect;
    }

    /// <summary>
    /// Win32 parks a minimized window at roughly (-32000, -32000). Any consumer
    /// that treats that as a real position will crop an empty region and then
    /// report "no text found", which points the investigation at OCR instead of
    /// at the minimized window.
    /// </summary>
    private static bool IsSentinelRect(RECT rect)
    {
        return rect.Left <= -32000 || rect.Top <= -32000;
    }

    private static bool IsMinimized(IntPtr hwnd)
    {
        return IsIconic(hwnd);
    }

    /// <summary>
    /// DWM extended frame bounds. GetWindowRect on a DPI-aware process still
    /// includes the invisible resize border on Windows 10/11, which is the single
    /// biggest source of "my click is off by 8-16 px" bugs.
    /// </summary>
    private static RECT FrameBounds(IntPtr hwnd)
    {
        RECT rect;
        try
        {
            if (DwmGetWindowAttribute(hwnd, DWMWA_EXTENDED_FRAME_BOUNDS, out rect, Marshal.SizeOf(typeof(RECT))) == 0)
            {
                if (rect.Right > rect.Left && rect.Bottom > rect.Top)
                {
                    return rect;
                }
            }
        }
        catch (Exception)
        {
        }
        return RawWindowRect(hwnd);
    }

    private static Dictionary<string, object> WindowDescriptor(IntPtr hwnd)
    {
        RECT frame = FrameBounds(hwnd);
        RECT raw = RawWindowRect(hwnd);
        // A minimized window reports a sentinel rectangle around (-32000, -32000)
        // with a tiny size. Handing that back as a normal frame is actively
        // harmful: a caller that passes it as a region gets an empty crop, and
        // OCR then "finds nothing" for reasons that look unrelated to the real
        // problem. Say so explicitly instead.
        bool frameValid = !IsMinimized(hwnd) && !IsSentinelRect(frame) && !IsSentinelRect(raw);
        POINT clientOrigin = new POINT();
        RECT client;
        clientOrigin.X = 0;
        clientOrigin.Y = 0;
        ClientToScreen(hwnd, ref clientOrigin);
        GetClientRect(hwnd, out client);

        Dictionary<string, object> item = new Dictionary<string, object>();
        item["handle"] = hwnd.ToInt64().ToString(CultureInfo.InvariantCulture);
        item["title"] = WindowTitle(hwnd);
        item["class_name"] = WindowClass(hwnd);
        item["pid"] = WindowProcessId(hwnd);
        item["visible"] = IsWindowVisible(hwnd);
        item["minimized"] = IsIconic(hwnd);
        item["frame_valid"] = frameValid;
        item["frame"] = RectObject(frame.Left, frame.Top, frame.Right - frame.Left, frame.Bottom - frame.Top);
        item["window_rect"] = RectObject(raw.Left, raw.Top, raw.Right - raw.Left, raw.Bottom - raw.Top);
        item["client_origin"] = PointObject(clientOrigin.X, clientOrigin.Y);
        item["client_size"] = RectObject(clientOrigin.X, clientOrigin.Y, client.Right - client.Left, client.Bottom - client.Top);
        return item;
    }

    private static List<IntPtr> TopLevelWindows()
    {
        List<IntPtr> handles = new List<IntPtr>();
        EnumWindowsProc callback = delegate(IntPtr hwnd, IntPtr lParam)
        {
            if (IsWindowVisible(hwnd))
            {
                handles.Add(hwnd);
            }
            return true;
        };
        EnumWindows(callback, IntPtr.Zero);
        return handles;
    }

    /// <summary>
    /// Bring a window to the foreground. SetForegroundWindow is restricted by the
    /// foreground lock, so attach our input queue to the current foreground thread
    /// first - the standard workaround, not a privilege escalation.
    /// </summary>
    private static bool ActivateWindow(IntPtr hwnd)
    {
        if (!IsWindow(hwnd))
        {
            return false;
        }
        if (IsIconic(hwnd))
        {
            ShowWindow(hwnd, 9); // SW_RESTORE
        }
        else
        {
            ShowWindow(hwnd, 5); // SW_SHOW
        }

        IntPtr foreground = GetForegroundWindow();
        bool attached = false;
        uint currentThread = GetCurrentThreadId();
        uint ignoredPid;
        uint targetThread = GetWindowThreadProcessId(foreground, out ignoredPid);
        if (foreground != hwnd && targetThread != 0 && targetThread != currentThread)
        {
            attached = AttachThreadInput(targetThread, currentThread, true);
        }
        try
        {
            BringWindowToTop(hwnd);
            bool ok = SetForegroundWindow(hwnd);
            if (!ok)
            {
                ShowWindow(hwnd, 9);
                ok = SetForegroundWindow(hwnd);
            }
            return ok;
        }
        finally
        {
            if (attached)
            {
                AttachThreadInput(targetThread, currentThread, false);
            }
        }
    }

    // ---------------------------------------------------------------------
    // Input injection
    // ---------------------------------------------------------------------

    private static uint SendMouseAbsolute(uint flags, int x, int y)
    {
        VirtualDesktop vd = CurrentVirtualDesktop();
        INPUT[] inputs = new INPUT[1];
        inputs[0].type = INPUT_MOUSE;
        inputs[0].u.mi.dx = NormalizeAxis(x, vd.X, vd.Width);
        inputs[0].u.mi.dy = NormalizeAxis(y, vd.Y, vd.Height);
        inputs[0].u.mi.mouseData = 0;
        inputs[0].u.mi.dwFlags = flags | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK | MOUSEEVENTF_MOVE;
        inputs[0].u.mi.time = 0;
        inputs[0].u.mi.dwExtraInfo = IntPtr.Zero;
        return SendInput(1, inputs, Marshal.SizeOf(typeof(INPUT)));
    }

    private static uint SendMouseButton(uint flag)
    {
        INPUT[] inputs = new INPUT[1];
        inputs[0].type = INPUT_MOUSE;
        inputs[0].u.mi.dx = 0;
        inputs[0].u.mi.dy = 0;
        inputs[0].u.mi.mouseData = 0;
        inputs[0].u.mi.dwFlags = flag;
        inputs[0].u.mi.time = 0;
        inputs[0].u.mi.dwExtraInfo = IntPtr.Zero;
        return SendInput(1, inputs, Marshal.SizeOf(typeof(INPUT)));
    }

    private static uint SendMouseWheel(uint flag, int delta)
    {
        INPUT[] inputs = new INPUT[1];
        inputs[0].type = INPUT_MOUSE;
        inputs[0].u.mi.dx = 0;
        inputs[0].u.mi.dy = 0;
        inputs[0].u.mi.mouseData = unchecked((uint)delta);
        inputs[0].u.mi.dwFlags = flag;
        inputs[0].u.mi.time = 0;
        inputs[0].u.mi.dwExtraInfo = IntPtr.Zero;
        return SendInput(1, inputs, Marshal.SizeOf(typeof(INPUT)));
    }

    private static POINT WaitForCursor(int x, int y, int timeoutMs)
    {
        Stopwatch watch = Stopwatch.StartNew();
        POINT point = CursorPosition();
        while (watch.ElapsedMilliseconds < timeoutMs)
        {
            point = CursorPosition();
            if (Math.Abs(point.X - x) <= 1 && Math.Abs(point.Y - y) <= 1)
            {
                return point;
            }
            Thread.Sleep(8);
        }
        return CursorPosition();
    }

    private static Dictionary<string, object> MovePointerTo(int x, int y)
    {
        POINT before = CursorPosition();
        SendMouseAbsolute(0, x, y);
        POINT after = WaitForCursor(x, y, 180);
        string method = "sendinput";

        if (Math.Abs(after.X - x) > 1 || Math.Abs(after.Y - y) > 1)
        {
            SetCursorPos(x, y);
            after = WaitForCursor(x, y, 180);
            method = "setcursorpos-fallback";
        }

        Dictionary<string, object> result = new Dictionary<string, object>();
        result["requested"] = PointObject(x, y);
        result["cursor_before"] = PointObject(before.X, before.Y);
        result["cursor_after"] = PointObject(after.X, after.Y);
        result["delta_x"] = after.X - x;
        result["delta_y"] = after.Y - y;
        result["verified"] = Math.Abs(after.X - x) <= 1 && Math.Abs(after.Y - y) <= 1;
        result["move_method"] = method;
        return result;
    }

    private static uint ButtonDownFlag(string button)
    {
        switch ((button ?? "left").ToLowerInvariant())
        {
            case "right":
                return MOUSEEVENTF_RIGHTDOWN;
            case "middle":
                return MOUSEEVENTF_MIDDLEDOWN;
            case "left":
                return MOUSEEVENTF_LEFTDOWN;
            default:
                throw new ArgumentException("unsupported mouse button: " + button);
        }
    }

    private static uint ButtonUpFlag(string button)
    {
        switch ((button ?? "left").ToLowerInvariant())
        {
            case "right":
                return MOUSEEVENTF_RIGHTUP;
            case "middle":
                return MOUSEEVENTF_MIDDLEUP;
            case "left":
                return MOUSEEVENTF_LEFTUP;
            default:
                throw new ArgumentException("unsupported mouse button: " + button);
        }
    }

    // ---------------------------------------------------------------------
    // Keyboard
    // ---------------------------------------------------------------------

    [DllImport("user32.dll")]
    private static extern uint MapVirtualKey(uint uCode, uint uMapType);

    private static readonly HashSet<int> ExtendedKeys = BuildExtendedKeySet();

    private static HashSet<int> BuildExtendedKeySet()
    {
        HashSet<int> keys = new HashSet<int>();
        keys.Add(0x21); // PageUp
        keys.Add(0x22); // PageDown
        keys.Add(0x23); // End
        keys.Add(0x24); // Home
        keys.Add(0x25); // Left
        keys.Add(0x26); // Up
        keys.Add(0x27); // Right
        keys.Add(0x28); // Down
        keys.Add(0x2D); // Insert
        keys.Add(0x2E); // Delete
        keys.Add(0x5B); // LWin
        keys.Add(0x5C); // RWin
        keys.Add(0x5D); // Apps
        keys.Add(0x6F); // Numpad divide
        keys.Add(0xA3); // Right Ctrl
        keys.Add(0xA5); // Right Alt
        return keys;
    }

    private static readonly Dictionary<string, int> NamedKeys = BuildNamedKeys();

    private static Dictionary<string, int> BuildNamedKeys()
    {
        Dictionary<string, int> map = new Dictionary<string, int>(StringComparer.OrdinalIgnoreCase);
        map["ctrl"] = 0x11;
        map["control"] = 0x11;
        map["shift"] = 0x10;
        map["alt"] = 0x12;
        map["win"] = 0x5B;
        map["meta"] = 0x5B;
        map["enter"] = 0x0D;
        map["return"] = 0x0D;
        map["esc"] = 0x1B;
        map["escape"] = 0x1B;
        map["space"] = 0x20;
        map["tab"] = 0x09;
        map["backspace"] = 0x08;
        map["back"] = 0x08;
        map["delete"] = 0x2E;
        map["del"] = 0x2E;
        map["insert"] = 0x2D;
        map["home"] = 0x24;
        map["end"] = 0x23;
        map["pageup"] = 0x21;
        map["pagedown"] = 0x22;
        map["up"] = 0x26;
        map["down"] = 0x28;
        map["left"] = 0x25;
        map["right"] = 0x27;
        map["capslock"] = 0x14;
        map["f1"] = 0x70;
        map["f2"] = 0x71;
        map["f3"] = 0x72;
        map["f4"] = 0x73;
        map["f5"] = 0x74;
        map["f6"] = 0x75;
        map["f7"] = 0x76;
        map["f8"] = 0x77;
        map["f9"] = 0x78;
        map["f10"] = 0x79;
        map["f11"] = 0x7A;
        map["f12"] = 0x7B;
        map["printscreen"] = 0x2C;
        map["scrolllock"] = 0x91;
        map["pause"] = 0x13;
        map["numlock"] = 0x90;
        map["apps"] = 0x5D;
        return map;
    }

    private static int ResolveVirtualKey(string key)
    {
        if (string.IsNullOrEmpty(key))
        {
            throw new ArgumentException("key is required");
        }
        string name = key.Trim();
        if (name.Length == 1)
        {
            char ch = char.ToUpperInvariant(name[0]);
            if ((ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'))
            {
                return ch;
            }
        }
        int mapped;
        if (NamedKeys.TryGetValue(name, out mapped))
        {
            return mapped;
        }
        throw new ArgumentException("unsupported key: " + key);
    }

    private static void SendVirtualKey(int vk, bool down)
    {
        INPUT[] inputs = new INPUT[1];
        inputs[0].type = INPUT_KEYBOARD;
        uint scan = MapVirtualKey((uint)vk, 0);
        uint flags = KEYEVENTF_SCANCODE;
        if (down)
        {
            flags |= 0;
        }
        else
        {
            flags |= KEYEVENTF_KEYUP;
        }
        if (ExtendedKeys.Contains(vk))
        {
            flags |= KEYEVENTF_EXTENDEDKEY;
        }
        inputs[0].u.ki.wVk = 0;
        inputs[0].u.ki.wScan = (ushort)scan;
        inputs[0].u.ki.dwFlags = flags;
        inputs[0].u.ki.time = 0;
        inputs[0].u.ki.dwExtraInfo = IntPtr.Zero;
        SendInput(1, inputs, Marshal.SizeOf(typeof(INPUT)));
    }

    private static int SendUnicodeText(string text)
    {
        int sent = 0;
        foreach (char ch in text)
        {
            INPUT[] inputs = new INPUT[2];
            inputs[0].type = INPUT_KEYBOARD;
            inputs[0].u.ki.wVk = 0;
            inputs[0].u.ki.wScan = ch;
            inputs[0].u.ki.dwFlags = KEYEVENTF_UNICODE;
            inputs[0].u.ki.time = 0;
            inputs[0].u.ki.dwExtraInfo = IntPtr.Zero;
            inputs[1] = inputs[0];
            inputs[1].u.ki.dwFlags = KEYEVENTF_UNICODE | KEYEVENTF_KEYUP;
            SendInput(2, inputs, Marshal.SizeOf(typeof(INPUT)));
            sent++;
            if (ch == '\n')
            {
                Thread.Sleep(15);
            }
        }
        return sent;
    }

    private static int SendClipboardText(string text)
    {
        string previous = null;
        try
        {
            previous = Clipboard.ContainsText() ? Clipboard.GetText() : null;
        }
        catch (Exception)
        {
        }
        Clipboard.SetText(text);
        Thread.Sleep(60);
        int ctrl = ResolveVirtualKey("ctrl");
        SendVirtualKey(ctrl, true);
        SendVirtualKey(ResolveVirtualKey("v"), true);
        SendVirtualKey(ResolveVirtualKey("v"), false);
        SendVirtualKey(ctrl, false);
        Thread.Sleep(120);
        try
        {
            if (previous == null)
            {
                Clipboard.Clear();
            }
            else
            {
                Clipboard.SetText(previous);
            }
        }
        catch (Exception)
        {
        }
        return text.Length;
    }

    // ---------------------------------------------------------------------
    // Screenshot + diff
    // ---------------------------------------------------------------------

    private static double DoubleValue(Dictionary<string, object> source, string key, double fallback)
    {
        object value;
        if (source == null || !source.TryGetValue(key, out value) || value == null)
        {
            return fallback;
        }
        double parsed;
        if (double.TryParse(Convert.ToString(value, CultureInfo.InvariantCulture), NumberStyles.Float, CultureInfo.InvariantCulture, out parsed))
        {
            return parsed;
        }
        return fallback;
    }

    private static string RequiredPath(Dictionary<string, object> request, string key)
    {
        string path = Str(request, key);
        if (string.IsNullOrEmpty(path))
        {
            throw new ArgumentException(key + " is required");
        }
        return Path.GetFullPath(path);
    }

    private static Dictionary<string, object> CaptureScreenshot(Dictionary<string, object> request)
    {
        string full = RequiredPath(request, "output_path");
        string directory = Path.GetDirectoryName(full);
        if (!string.IsNullOrEmpty(directory))
        {
            Directory.CreateDirectory(directory);
        }

        IntPtr handle = HandleValue(request, "handle", false);
        VirtualDesktop vd = CurrentVirtualDesktop();
        int originX = vd.X;
        int originY = vd.Y;
        int width = vd.Width;
        int height = vd.Height;
        string scope = "virtual_screen";
        string captureMethod = "copy_from_screen";

        Bitmap bitmap = null;
        try
        {
            if (handle != IntPtr.Zero)
            {
                if (!IsWindow(handle))
                {
                    throw new ArgumentException("window handle is not a valid window");
                }
                RECT frame = FrameBounds(handle);
                if (IsMinimized(handle) || IsSentinelRect(frame))
                {
                    throw new InvalidOperationException(
                        "cannot capture a minimized window; restore it first (computer_window activate)");
                }
                originX = frame.Left;
                originY = frame.Top;
                width = Math.Max(1, frame.Right - frame.Left);
                height = Math.Max(1, frame.Bottom - frame.Top);
                scope = "window";
                bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
                captureMethod = "print_window";
                using (Graphics graphics = Graphics.FromImage(bitmap))
                {
                    IntPtr hdc = graphics.GetHdc();
                    bool printed;
                    try
                    {
                        printed = PrintWindow(handle, hdc, PW_RENDERFULLCONTENT);
                    }
                    finally
                    {
                        graphics.ReleaseHdc(hdc);
                    }
                    if (!printed)
                    {
                        graphics.CopyFromScreen(originX, originY, 0, 0, new Size(width, height), CopyPixelOperation.SourceCopy);
                        captureMethod = "copy_from_screen_crop";
                    }
                }
            }
            else
            {
                bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
                using (Graphics graphics = Graphics.FromImage(bitmap))
                {
                    graphics.CopyFromScreen(originX, originY, 0, 0, new Size(width, height), CopyPixelOperation.SourceCopy);
                }
            }

            bitmap.Save(full, ImageFormat.Png);
        }
        finally
        {
            if (bitmap != null)
            {
                bitmap.Dispose();
            }
        }

        FileInfo info = new FileInfo(full);
        Dictionary<string, object> result = new Dictionary<string, object>();
        result["path"] = full;
        result["width"] = width;
        result["height"] = height;
        result["size"] = info.Length;
        result["scope"] = scope;
        result["capture_method"] = captureMethod;
        result["coordinate_space"] = "screenshot";
        result["screen_origin"] = PointObject(originX, originY);
        result["scale"] = 1.0;
        result["geometry_hash"] = GeometryHash(vd);
        result["screenshot_id"] = string.Format(
            CultureInfo.InvariantCulture,
            "{0}-{1}-{2}-{3}-{4}",
            scope,
            originX,
            originY,
            width,
            height);
        result["captured_at"] = DateTime.Now.ToString("o", CultureInfo.InvariantCulture);
        return result;
    }

    private static Dictionary<string, object> ImageDiff(Dictionary<string, object> request)
    {
        string beforePath = RequiredPath(request, "before_path");
        string afterPath = RequiredPath(request, "after_path");
        int tolerance = IntValue(request, "tolerance", 6);
        double threshold = DoubleValue(request, "threshold", 0.002);
        string diffPath = Str(request, "diff_path");

        using (Bitmap before = new Bitmap(beforePath))
        using (Bitmap after = new Bitmap(afterPath))
        {
            Dictionary<string, object> result = new Dictionary<string, object>();
            result["before_path"] = beforePath;
            result["after_path"] = afterPath;
            result["tolerance"] = tolerance;

            if (before.Width != after.Width || before.Height != after.Height)
            {
                result["ok"] = false;
                result["code"] = "size_mismatch";
                result["error"] = "images have different dimensions";
                result["before_size"] = PointObject(before.Width, before.Height);
                result["after_size"] = PointObject(after.Width, after.Height);
                return result;
            }

            int width = before.Width;
            int height = before.Height;

            // Optional region of interest, in screenshot coordinates. Without it a
            // full-screen diff also picks up unrelated background repaints (browser
            // video, clocks, notifications), which makes "did my click work?"
            // indistinguishable from "something else moved".
            int regionX = 0;
            int regionY = 0;
            int regionWidth = width;
            int regionHeight = height;
            object rawRegion;
            if (request.TryGetValue("region", out rawRegion) && rawRegion is Dictionary<string, object>)
            {
                Dictionary<string, object> region = (Dictionary<string, object>)rawRegion;
                regionX = Math.Max(0, Math.Min(width - 1, IntValue(region, "x", 0)));
                regionY = Math.Max(0, Math.Min(height - 1, IntValue(region, "y", 0)));
                regionWidth = IntValue(region, "width", width - regionX);
                regionHeight = IntValue(region, "height", height - regionY);
                regionWidth = Math.Max(1, Math.Min(width - regionX, regionWidth));
                regionHeight = Math.Max(1, Math.Min(height - regionY, regionHeight));
                result["region"] = RectObject(regionX, regionY, regionWidth, regionHeight);
            }

            long total = (long)regionWidth * regionHeight;
            Rectangle rect = new Rectangle(0, 0, width, height);
            BitmapData dataBefore = before.LockBits(rect, ImageLockMode.ReadOnly, PixelFormat.Format32bppArgb);
            BitmapData dataAfter = after.LockBits(rect, ImageLockMode.ReadOnly, PixelFormat.Format32bppArgb);

            Bitmap diffBitmap = null;
            BitmapData dataDiff = null;
            byte[] diffRow = null;
            if (!string.IsNullOrEmpty(diffPath))
            {
                diffBitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb);
                using (Graphics graphics = Graphics.FromImage(diffBitmap))
                {
                    graphics.Clear(Color.FromArgb(255, 16, 16, 20));
                }
                dataDiff = diffBitmap.LockBits(rect, ImageLockMode.WriteOnly, PixelFormat.Format32bppArgb);
                diffRow = new byte[dataDiff.Stride];
            }
            byte[] unchangedRow = null;
            if (diffRow != null)
            {
                unchangedRow = new byte[diffRow.Length];
                for (int index = 0; index + 3 < unchangedRow.Length; index += 4)
                {
                    unchangedRow[index] = 20;
                    unchangedRow[index + 1] = 16;
                    unchangedRow[index + 2] = 16;
                    unchangedRow[index + 3] = 255;
                }
            }

            long changedPixels = 0;
            int minX = int.MaxValue;
            int minY = int.MaxValue;
            int maxX = -1;
            int maxY = -1;
            // A single stray pixel (cursor shadow, clock tick, gradient dither)
            // otherwise stretches the bounding box across the whole screen and
            // makes the evidence useless. Track per-row/per-column density so the
            // reported box describes the area that actually changed.
            int[] rowDensity = new int[height];
            int[] columnDensity = new int[width];
            try
            {
                byte[] rowBefore = new byte[dataBefore.Stride];
                byte[] rowAfter = new byte[dataAfter.Stride];
                for (int y = 0; y < height; y++)
                {
                    Marshal.Copy(IntPtr.Add(dataBefore.Scan0, y * dataBefore.Stride), rowBefore, 0, dataBefore.Stride);
                    Marshal.Copy(IntPtr.Add(dataAfter.Scan0, y * dataAfter.Stride), rowAfter, 0, dataAfter.Stride);
                    if (unchangedRow != null)
                    {
                        Array.Copy(unchangedRow, diffRow, diffRow.Length);
                    }
                    if (y < regionY || y >= regionY + regionHeight)
                    {
                        if (diffRow != null)
                        {
                            Marshal.Copy(diffRow, 0, IntPtr.Add(dataDiff.Scan0, y * dataDiff.Stride), dataDiff.Stride);
                        }
                        continue;
                    }
                    for (int x = regionX; x < regionX + regionWidth; x++)
                    {
                        int offset = x * 4;
                        int db = Math.Abs(rowBefore[offset] - rowAfter[offset]);
                        int dg = Math.Abs(rowBefore[offset + 1] - rowAfter[offset + 1]);
                        int dr = Math.Abs(rowBefore[offset + 2] - rowAfter[offset + 2]);
                        bool changed = db > tolerance || dg > tolerance || dr > tolerance;
                        if (changed)
                        {
                            changedPixels++;
                            rowDensity[y]++;
                            columnDensity[x]++;
                            if (x < minX) minX = x;
                            if (x > maxX) maxX = x;
                            if (y < minY) minY = y;
                            if (y > maxY) maxY = y;
                            if (diffRow != null)
                            {
                                diffRow[offset] = 0;
                                diffRow[offset + 1] = 40;
                                diffRow[offset + 2] = 255;
                                diffRow[offset + 3] = 255;
                            }
                        }
                    }
                    if (diffRow != null)
                    {
                        Marshal.Copy(diffRow, 0, IntPtr.Add(dataDiff.Scan0, y * dataDiff.Stride), dataDiff.Stride);
                    }
                }
            }
            finally
            {
                before.UnlockBits(dataBefore);
                after.UnlockBits(dataAfter);
                if (diffBitmap != null)
                {
                    diffBitmap.UnlockBits(dataDiff);
                }
            }

            double ratio = total == 0 ? 0 : (double)changedPixels / total;
            long minimumChanged = Math.Max(20L, (long)(total * threshold));
            result["changed_pixels"] = changedPixels;
            result["total_pixels"] = total;
            result["changed_ratio"] = Math.Round(ratio, 6);
            result["threshold"] = threshold;
            result["changed"] = changedPixels >= minimumChanged;
            result["bounding_box"] = changedPixels > 0
                ? RectObject(minX, minY, maxX - minX + 1, maxY - minY + 1)
                : RectObject(0, 0, 0, 0);
            if (changedPixels > 0)
            {
                result["dense_bounding_box"] = DenseBounds(rowDensity, columnDensity, minX, minY, maxX, maxY);
            }

            if (diffBitmap != null)
            {
                string fullDiff = Path.GetFullPath(diffPath);
                string diffDirectory = Path.GetDirectoryName(fullDiff);
                if (!string.IsNullOrEmpty(diffDirectory))
                {
                    Directory.CreateDirectory(diffDirectory);
                }
                diffBitmap.Save(fullDiff, ImageFormat.Png);
                result["diff_path"] = fullDiff;
            }

            if (diffBitmap != null)
            {
                diffBitmap.Dispose();
            }
            return result;
        }
    }

    /// <summary>
    /// Bounding box of the changed pixels after discarding rows and columns that
    /// carry only isolated noise. Falls back to the raw bounds when the change is
    /// genuinely thin (for example a one pixel border).
    /// </summary>
    private static Dictionary<string, object> DenseBounds(int[] rowDensity, int[] columnDensity, int minX, int minY, int maxX, int maxY)
    {
        const int minimumDensity = 3;
        int denseTop = int.MaxValue;
        int denseBottom = -1;
        int denseLeft = int.MaxValue;
        int denseRight = -1;
        for (int y = 0; y < rowDensity.Length; y++)
        {
            if (rowDensity[y] >= minimumDensity)
            {
                if (y < denseTop) denseTop = y;
                denseBottom = y;
            }
        }
        for (int x = 0; x < columnDensity.Length; x++)
        {
            if (columnDensity[x] >= minimumDensity)
            {
                if (x < denseLeft) denseLeft = x;
                denseRight = x;
            }
        }
        if (denseBottom < 0 || denseRight < 0)
        {
            return RectObject(minX, minY, maxX - minX + 1, maxY - minY + 1);
        }
        return RectObject(denseLeft, denseTop, denseRight - denseLeft + 1, denseBottom - denseTop + 1);
    }

    // ---------------------------------------------------------------------
    // UI Automation
    // ---------------------------------------------------------------------

    private static List<string> ElementPatterns(AutomationElement element)
    {
        List<string> patterns = new List<string>();
        object pattern;
        try
        {
            if (element.TryGetCurrentPattern(InvokePattern.Pattern, out pattern)) patterns.Add("invoke");
            if (element.TryGetCurrentPattern(TogglePattern.Pattern, out pattern)) patterns.Add("toggle");
            if (element.TryGetCurrentPattern(ExpandCollapsePattern.Pattern, out pattern)) patterns.Add("expand_collapse");
            if (element.TryGetCurrentPattern(SelectionItemPattern.Pattern, out pattern)) patterns.Add("selection_item");
            if (element.TryGetCurrentPattern(ValuePattern.Pattern, out pattern)) patterns.Add("value");
            if (element.TryGetCurrentPattern(ScrollItemPattern.Pattern, out pattern)) patterns.Add("scroll_item");
        }
        catch (Exception)
        {
        }
        return patterns;
    }

    private static Dictionary<string, object> DescribeElement(AutomationElement element, int depth)
    {
        Dictionary<string, object> node = new Dictionary<string, object>();
        node["depth"] = depth;
        try
        {
            AutomationElement.AutomationElementInformation current = element.Current;
            node["name"] = current.Name ?? string.Empty;
            node["control_type"] = current.ControlType == null ? string.Empty : current.ControlType.ProgrammaticName.Replace("ControlType.", string.Empty);
            node["automation_id"] = current.AutomationId ?? string.Empty;
            node["class_name"] = current.ClassName ?? string.Empty;
            node["enabled"] = current.IsEnabled;
            node["offscreen"] = current.IsOffscreen;
            System.Windows.Rect bounds = current.BoundingRectangle;
            if (!bounds.IsEmpty)
            {
                node["bounds"] = RectObject(
                    (int)Math.Round(bounds.Left),
                    (int)Math.Round(bounds.Top),
                    (int)Math.Round(bounds.Width),
                    (int)Math.Round(bounds.Height));
                node["coordinate_space"] = "screen";
            }
        }
        catch (Exception exception)
        {
            node["error"] = exception.GetType().Name;
        }
        List<string> patterns = ElementPatterns(element);
        if (patterns.Count > 0)
        {
            node["patterns"] = patterns;
        }
        return node;
    }

    private static Dictionary<string, object> UiTree(Dictionary<string, object> request)
    {
        IntPtr handle = HandleValue(request, "handle", false);
        int maxDepth = IntValue(request, "max_depth", 4);
        int maxNodes = IntValue(request, "max_nodes", 300);
        string filter = Str(request, "filter");
        bool includeOffscreen = BoolValue(request, "include_offscreen", false);

        AutomationElement root = handle == IntPtr.Zero
            ? AutomationElement.RootElement
            : AutomationElement.FromHandle(handle);
        if (root == null)
        {
            throw new ArgumentException("unable to resolve automation root for the given handle");
        }

        List<Dictionary<string, object>> nodes = new List<Dictionary<string, object>>();
        Queue<KeyValuePair<AutomationElement, int>> queue = new Queue<KeyValuePair<AutomationElement, int>>();
        queue.Enqueue(new KeyValuePair<AutomationElement, int>(root, 0));
        TreeWalker walker = TreeWalker.ControlViewWalker;
        bool truncated = false;

        while (queue.Count > 0)
        {
            KeyValuePair<AutomationElement, int> entry = queue.Dequeue();
            if (nodes.Count >= maxNodes)
            {
                truncated = true;
                break;
            }
            Dictionary<string, object> node = DescribeElement(entry.Key, entry.Value);
            bool offscreen = node.ContainsKey("offscreen") && node["offscreen"] is bool && (bool)node["offscreen"];
            bool matches = string.IsNullOrEmpty(filter) || MatchesFilter(node, filter);
            if ((includeOffscreen || !offscreen) && matches)
            {
                nodes.Add(node);
            }
            if (entry.Value >= maxDepth)
            {
                continue;
            }
            try
            {
                AutomationElement child = walker.GetFirstChild(entry.Key);
                while (child != null)
                {
                    queue.Enqueue(new KeyValuePair<AutomationElement, int>(child, entry.Value + 1));
                    child = walker.GetNextSibling(child);
                }
            }
            catch (Exception)
            {
            }
        }

        Dictionary<string, object> result = new Dictionary<string, object>();
        result["nodes"] = nodes;
        result["count"] = nodes.Count;
        result["truncated"] = truncated;
        result["coordinate_space"] = "screen";
        result["handle"] = handle == IntPtr.Zero ? "" : handle.ToInt64().ToString(CultureInfo.InvariantCulture);
        result["geometry_hash"] = GeometryHash(CurrentVirtualDesktop());
        return result;
    }

    private static bool MatchesFilter(Dictionary<string, object> node, string filter)
    {
        string needle = filter.ToLowerInvariant();
        string[] keys = new string[] { "name", "automation_id", "class_name", "control_type" };
        foreach (string key in keys)
        {
            object value;
            if (node.TryGetValue(key, out value) && value != null)
            {
                string text = Convert.ToString(value, CultureInfo.InvariantCulture);
                if (!string.IsNullOrEmpty(text) && text.ToLowerInvariant().Contains(needle))
                {
                    return true;
                }
            }
        }
        return false;
    }

    private static AutomationElement FindElement(IntPtr handle, string name, string automationId, string controlType, int timeoutMs)
    {
        AutomationElement root = handle == IntPtr.Zero
            ? AutomationElement.RootElement
            : AutomationElement.FromHandle(handle);
        Stopwatch watch = Stopwatch.StartNew();
        while (true)
        {
            AutomationElement found = SearchElement(root, name, automationId, controlType);
            if (found != null)
            {
                return found;
            }
            if (watch.ElapsedMilliseconds >= timeoutMs)
            {
                return null;
            }
            Thread.Sleep(120);
        }
    }

    private static AutomationElement SearchElement(AutomationElement root, string name, string automationId, string controlType)
    {
        if (root == null)
        {
            return null;
        }
        try
        {
            if (!string.IsNullOrEmpty(name) && string.Equals(root.Current.Name, name, StringComparison.OrdinalIgnoreCase))
            {
                return root;
            }
            if (!string.IsNullOrEmpty(automationId) && string.Equals(root.Current.AutomationId, automationId, StringComparison.OrdinalIgnoreCase))
            {
                return root;
            }
        }
        catch (Exception)
        {
        }

        TreeWalker walker = TreeWalker.ControlViewWalker;
        try
        {
            AutomationElement child = walker.GetFirstChild(root);
            while (child != null)
            {
                try
                {
                    string childName = child.Current.Name;
                    string childId = child.Current.AutomationId;
                    string childType = child.Current.ControlType == null ? string.Empty : child.Current.ControlType.ProgrammaticName;
                    bool nameOk = string.IsNullOrEmpty(name) || (!string.IsNullOrEmpty(childName) && childName.IndexOf(name, StringComparison.OrdinalIgnoreCase) >= 0);
                    bool idOk = string.IsNullOrEmpty(automationId) || string.Equals(childId, automationId, StringComparison.OrdinalIgnoreCase);
                    bool typeOk = string.IsNullOrEmpty(controlType) || childType.IndexOf(controlType, StringComparison.OrdinalIgnoreCase) >= 0;
                    if ((!string.IsNullOrEmpty(name) || !string.IsNullOrEmpty(automationId)) && nameOk && idOk && typeOk)
                    {
                        return child;
                    }
                }
                catch (Exception)
                {
                }
                AutomationElement nested = SearchElement(child, name, automationId, controlType);
                if (nested != null)
                {
                    return nested;
                }
                child = walker.GetNextSibling(child);
            }
        }
        catch (Exception)
        {
        }
        return null;
    }

    private static bool TryCenterOf(AutomationElement element, out int x, out int y)
    {
        x = 0;
        y = 0;
        try
        {
            System.Windows.Rect bounds = element.Current.BoundingRectangle;
            if (bounds.IsEmpty || bounds.Width < 1 || bounds.Height < 1)
            {
                return false;
            }
            x = (int)Math.Round(bounds.Left + bounds.Width / 2);
            y = (int)Math.Round(bounds.Top + bounds.Height / 2);
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private static Dictionary<string, object> UiInvoke(Dictionary<string, object> request)
    {
        IntPtr handle = HandleValue(request, "handle", false);
        string name = Str(request, "name");
        string automationId = Str(request, "automation_id");
        string controlType = Str(request, "control_type");
        int timeoutMs = IntValue(request, "timeout_ms", 2000);

        Dictionary<string, object> result = new Dictionary<string, object>();
        AutomationElement element = null;

        if (string.IsNullOrEmpty(name) && string.IsNullOrEmpty(automationId) && request.ContainsKey("x") && request.ContainsKey("y"))
        {
            Point point = new Point(RequiredInt(request, "x"), RequiredInt(request, "y"));
            element = AutomationElement.FromPoint(new System.Windows.Point(point.X, point.Y));
        }
        else
        {
            element = FindElement(handle, name, automationId, controlType, timeoutMs);
        }

        if (element == null)
        {
            result["ok"] = false;
            result["code"] = "element_not_found";
            result["error"] = "no matching UI Automation element";
            return result;
        }

        result["element"] = DescribeElement(element, 0);
        object pattern;
        try
        {
            if (element.TryGetCurrentPattern(InvokePattern.Pattern, out pattern))
            {
                ((InvokePattern)pattern).Invoke();
                result["method"] = "invoke_pattern";
                result["invoked"] = true;
                return result;
            }
            if (element.TryGetCurrentPattern(TogglePattern.Pattern, out pattern))
            {
                ((TogglePattern)pattern).Toggle();
                result["method"] = "toggle_pattern";
                result["invoked"] = true;
                return result;
            }
            if (element.TryGetCurrentPattern(SelectionItemPattern.Pattern, out pattern))
            {
                ((SelectionItemPattern)pattern).Select();
                result["method"] = "selection_item_pattern";
                result["invoked"] = true;
                return result;
            }
            if (element.TryGetCurrentPattern(ExpandCollapsePattern.Pattern, out pattern))
            {
                ((ExpandCollapsePattern)pattern).Expand();
                result["method"] = "expand_collapse_pattern";
                result["invoked"] = true;
                return result;
            }
        }
        catch (Exception exception)
        {
            result["pattern_error"] = exception.GetType().Name + ": " + exception.Message;
        }

        int centerX;
        int centerY;
        if (!TryCenterOf(element, out centerX, out centerY))
        {
            result["ok"] = false;
            result["code"] = "no_actionable_pattern";
            result["error"] = "element exposes no invoke pattern and has no usable bounds";
            return result;
        }

        Dictionary<string, object> move = MovePointerTo(centerX, centerY);
        SendMouseButton(MOUSEEVENTF_LEFTDOWN);
        Thread.Sleep(40);
        SendMouseButton(MOUSEEVENTF_LEFTUP);
        result["invoked"] = true;
        result["method"] = "synthetic_click_center";
        result["clicked_point"] = PointObject(centerX, centerY);
        result["move"] = move;
        return result;
    }

    // ---------------------------------------------------------------------
    // Coordinate space resolution
    // ---------------------------------------------------------------------

    private static bool TryResolveScreenPoint(Dictionary<string, object> request, int x, int y, out int screenX, out int screenY, out string error)
    {
        string space = (Str(request, "coordinate_space") ?? "screen").Trim().ToLowerInvariant();
        screenX = x;
        screenY = y;
        error = null;
        VirtualDesktop vd = CurrentVirtualDesktop();

        switch (space)
        {
            case "":
            case "screen":
            case "desktop":
                return true;
            case "screenshot":
            case "image":
                screenX = vd.X + x;
                screenY = vd.Y + y;
                return true;
            case "window":
            case "frame":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                RECT frame = FrameBounds(handle);
                screenX = frame.Left + x;
                screenY = frame.Top + y;
                return true;
            }
            case "client":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                POINT origin = new POINT();
                origin.X = 0;
                origin.Y = 0;
                ClientToScreen(handle, ref origin);
                screenX = origin.X + x;
                screenY = origin.Y + y;
                return true;
            }
            default:
                error = "unsupported coordinate_space: " + space;
                return false;
        }
    }

    // ---------------------------------------------------------------------
    // Tool handlers
    // ---------------------------------------------------------------------

    private static Dictionary<string, object> HandleObserve(Dictionary<string, object> request)
    {
        string action = (Str(request, "action") ?? string.Empty).Trim().ToLowerInvariant();
        VirtualDesktop vd = CurrentVirtualDesktop();
        switch (action)
        {
            case "screen_info":
            {
                POINT cursor = CursorPosition();
                Dictionary<string, object> result = new Dictionary<string, object>();
                result["width"] = vd.Width;
                result["height"] = vd.Height;
                result["origin"] = PointObject(vd.X, vd.Y);
                result["cursor"] = PointObject(cursor.X, cursor.Y);
                result["monitors"] = EnumerateMonitors();
                result["dpi_aware"] = true;
                result["coordinate_space"] = "screen";
                result["geometry_hash"] = GeometryHash(vd);
                return result;
            }
            case "cursor_position":
            {
                POINT cursor = CursorPosition();
                Dictionary<string, object> result = new Dictionary<string, object>();
                result["x"] = cursor.X;
                result["y"] = cursor.Y;
                result["coordinate_space"] = "screen";
                result["geometry_hash"] = GeometryHash(vd);
                return result;
            }
            case "screenshot":
            case "capture":
                return CaptureScreenshot(request);
            case "image_diff":
            case "diff":
                return ImageDiff(request);
            case "active_window":
            {
                IntPtr handle = GetForegroundWindow();
                if (handle == IntPtr.Zero)
                {
                    Dictionary<string, object> empty = new Dictionary<string, object>();
                    empty["title"] = string.Empty;
                    empty["handle"] = string.Empty;
                    return empty;
                }
                Dictionary<string, object> descriptor = WindowDescriptor(handle);
                descriptor["foreground"] = true;
                return descriptor;
            }
            case "list_windows":
            {
                string filter = Str(request, "title");
                List<Dictionary<string, object>> windows = new List<Dictionary<string, object>>();
                foreach (IntPtr handle in TopLevelWindows())
                {
                    string title = WindowTitle(handle);
                    if (string.IsNullOrWhiteSpace(title))
                    {
                        continue;
                    }
                    if (!string.IsNullOrEmpty(filter) && title.IndexOf(filter, StringComparison.OrdinalIgnoreCase) < 0)
                    {
                        continue;
                    }
                    windows.Add(WindowDescriptor(handle));
                }
                Dictionary<string, object> result = new Dictionary<string, object>();
                result["windows"] = windows;
                result["count"] = windows.Count;
                result["geometry_hash"] = GeometryHash(vd);
                return result;
            }
            case "window_info":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                return WindowDescriptor(handle);
            }
            case "ui_tree":
            case "find_text":
                return UiTree(request);
            default:
                throw new ArgumentException("unsupported observe action: " + action);
        }
    }

    private static Dictionary<string, object> HandlePointer(Dictionary<string, object> request)
    {
        string action = (Str(request, "action") ?? string.Empty).Trim().ToLowerInvariant();
        string button = Str(request, "button") ?? "left";
        VirtualDesktop vd = CurrentVirtualDesktop();
        Dictionary<string, object> result = new Dictionary<string, object>();
        result["action"] = action;
        result["button"] = button;
        result["coordinate_space"] = (Str(request, "coordinate_space") ?? "screen").ToLowerInvariant();
        result["geometry_hash"] = GeometryHash(vd);

        switch (action)
        {
            case "move":
            case "move_pointer":
            {
                int sx;
                int sy;
                string error;
                if (!TryResolveScreenPoint(request, RequiredInt(request, "x"), RequiredInt(request, "y"), out sx, out sy, out error))
                {
                    throw new ArgumentException(error);
                }
                result["screen_point"] = PointObject(sx, sy);
                Merge(result, MovePointerTo(sx, sy));
                return result;
            }
            case "click":
            case "double_click":
            case "right_click":
            case "middle_click":
            {
                if (action == "right_click")
                {
                    button = "right";
                }
                else if (action == "middle_click")
                {
                    button = "middle";
                }
                int sx;
                int sy;
                string error;
                if (!TryResolveScreenPoint(request, RequiredInt(request, "x"), RequiredInt(request, "y"), out sx, out sy, out error))
                {
                    throw new ArgumentException(error);
                }
                Dictionary<string, object> move = MovePointerTo(sx, sy);
                int clicks = action == "double_click" ? 2 : 1;
                for (int index = 0; index < clicks; index++)
                {
                    SendMouseButton(ButtonDownFlag(button));
                    Thread.Sleep(45);
                    SendMouseButton(ButtonUpFlag(button));
                    if (clicks > 1 && index == 0)
                    {
                        Thread.Sleep(70);
                    }
                }
                POINT after = CursorPosition();
                result["button"] = button;
                result["screen_point"] = PointObject(sx, sy);
                result["cursor_after"] = PointObject(after.X, after.Y);
                result["clicks"] = clicks;
                Merge(result, move);
                return result;
            }
            case "drag":
            {
                int startX;
                int startY;
                int endX;
                int endY;
                string error;
                if (!TryResolveScreenPoint(request, RequiredInt(request, "start_x"), RequiredInt(request, "start_y"), out startX, out startY, out error))
                {
                    throw new ArgumentException(error);
                }
                if (!TryResolveScreenPoint(request, RequiredInt(request, "end_x"), RequiredInt(request, "end_y"), out endX, out endY, out error))
                {
                    throw new ArgumentException(error);
                }
                Merge(result, MovePointerTo(startX, startY));
                SendMouseButton(ButtonDownFlag(button));
                Thread.Sleep(80);
                int steps = 12;
                for (int step = 1; step <= steps; step++)
                {
                    int x = startX + (endX - startX) * step / steps;
                    int y = startY + (endY - startY) * step / steps;
                    SendMouseAbsolute(0, x, y);
                    Thread.Sleep(16);
                }
                Thread.Sleep(60);
                SendMouseButton(ButtonUpFlag(button));
                result["start"] = PointObject(startX, startY);
                result["end"] = PointObject(endX, endY);
                return result;
            }
            case "scroll":
            {
                int dy = IntValue(request, "dy", 0);
                int dx = IntValue(request, "dx", 0);
                if (request.ContainsKey("x") && request.ContainsKey("y"))
                {
                    int sx;
                    int sy;
                    string error;
                    if (TryResolveScreenPoint(request, RequiredInt(request, "x"), RequiredInt(request, "y"), out sx, out sy, out error))
                    {
                        Merge(result, MovePointerTo(sx, sy));
                    }
                }
                if (dy != 0)
                {
                    SendMouseWheel(MOUSEEVENTF_WHEEL, dy * 120);
                }
                if (dx != 0)
                {
                    SendMouseWheel(MOUSEEVENTF_HWHEEL, dx * 120);
                }
                result["dx"] = dx;
                result["dy"] = dy;
                return result;
            }
            case "mouse_down":
            {
                if (request.ContainsKey("x") && request.ContainsKey("y"))
                {
                    int sx;
                    int sy;
                    string error;
                    if (!TryResolveScreenPoint(request, RequiredInt(request, "x"), RequiredInt(request, "y"), out sx, out sy, out error))
                    {
                        throw new ArgumentException(error);
                    }
                    Merge(result, MovePointerTo(sx, sy));
                }
                SendMouseButton(ButtonDownFlag(button));
                return result;
            }
            case "mouse_up":
            {
                if (request.ContainsKey("x") && request.ContainsKey("y"))
                {
                    int sx;
                    int sy;
                    string error;
                    if (!TryResolveScreenPoint(request, RequiredInt(request, "x"), RequiredInt(request, "y"), out sx, out sy, out error))
                    {
                        throw new ArgumentException(error);
                    }
                    Merge(result, MovePointerTo(sx, sy));
                }
                SendMouseButton(ButtonUpFlag(button));
                return result;
            }
            case "invoke_element":
                return UiInvoke(request);
            default:
                throw new ArgumentException("unsupported pointer action: " + action);
        }
    }

    private static void Merge(Dictionary<string, object> target, Dictionary<string, object> source)
    {
        foreach (KeyValuePair<string, object> pair in source)
        {
            target[pair.Key] = pair.Value;
        }
    }

    private static Dictionary<string, object> HandleKeyboard(Dictionary<string, object> request)
    {
        string action = (Str(request, "action") ?? string.Empty).Trim().ToLowerInvariant();
        int count = 0;
        switch (action)
        {
            case "type":
            case "text":
            {
                string text = Str(request, "text");
                if (text == null)
                {
                    throw new ArgumentException("text is required");
                }
                string method = (Str(request, "method") ?? "unicode").Trim().ToLowerInvariant();
                if (method == "clipboard" || method == "paste")
                {
                    count = SendClipboardText(text);
                }
                else
                {
                    count = SendUnicodeText(text);
                }
                break;
            }
            case "press":
            {
                int vk = ResolveVirtualKey(Str(request, "key"));
                SendVirtualKey(vk, true);
                Thread.Sleep(30);
                SendVirtualKey(vk, false);
                count = 1;
                break;
            }
            case "hotkey":
            {
                object raw;
                if (!request.TryGetValue("keys", out raw) || raw == null)
                {
                    throw new ArgumentException("keys is required");
                }
                object[] items = raw as object[];
                if (items == null)
                {
                    throw new ArgumentException("keys must be a non-empty array");
                }
                List<int> keys = new List<int>();
                foreach (object item in items)
                {
                    keys.Add(ResolveVirtualKey(Convert.ToString(item, CultureInfo.InvariantCulture)));
                }
                if (keys.Count == 0)
                {
                    throw new ArgumentException("keys must be a non-empty array");
                }
                for (int index = 0; index < keys.Count; index++)
                {
                    SendVirtualKey(keys[index], true);
                    Thread.Sleep(20);
                }
                for (int index = keys.Count - 1; index >= 0; index--)
                {
                    SendVirtualKey(keys[index], false);
                    Thread.Sleep(20);
                }
                count = keys.Count;
                break;
            }
            case "key_down":
                SendVirtualKey(ResolveVirtualKey(Str(request, "key")), true);
                count = 1;
                break;
            case "key_up":
                SendVirtualKey(ResolveVirtualKey(Str(request, "key")), false);
                count = 1;
                break;
            default:
                throw new ArgumentException("unsupported keyboard action: " + action);
        }

        Dictionary<string, object> result = new Dictionary<string, object>();
        result["action"] = action;
        result["count"] = count;
        return result;
    }

    private static Dictionary<string, object> HandleWindow(Dictionary<string, object> request)
    {
        string action = (Str(request, "action") ?? string.Empty).Trim().ToLowerInvariant();
        Dictionary<string, object> result = new Dictionary<string, object>();
        result["action"] = action;
        switch (action)
        {
            case "open":
            {
                string target = Str(request, "target");
                if (string.IsNullOrEmpty(target))
                {
                    throw new ArgumentException("target is required");
                }
                ProcessStartInfo startInfo = new ProcessStartInfo();
                startInfo.FileName = target;
                startInfo.UseShellExecute = true;
                Process process = Process.Start(startInfo);
                result["changed"] = true;
                result["target"] = target;
                if (process != null)
                {
                    result["pid"] = process.Id;
                }
                return result;
            }
            case "activate":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                bool changed = ActivateWindow(handle);
                Thread.Sleep(IntValue(request, "settle_ms", 180));
                IntPtr nowForeground = GetForegroundWindow();
                // Restoring a minimized window is asynchronous: poll briefly so
                // callers get usable geometry from this same response instead of
                // the (-32000, -32000) sentinel they would otherwise inherit.
                for (int attempt = 0; attempt < 20 && IsMinimized(handle); attempt++)
                {
                    Thread.Sleep(50);
                }
                result["changed"] = changed;
                result["handle"] = handle.ToInt64().ToString(CultureInfo.InvariantCulture);
                result["foreground_handle"] = nowForeground.ToInt64().ToString(CultureInfo.InvariantCulture);
                result["foreground_title"] = WindowTitle(nowForeground);
                result["active"] = nowForeground == handle;
                result["minimized"] = IsMinimized(handle);
                Dictionary<string, object> descriptor = WindowDescriptor(handle);
                result["frame_valid"] = descriptor["frame_valid"];
                result["window"] = descriptor;
                return result;
            }
            case "minimize":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                result["changed"] = ShowWindow(handle, 6);
                result["handle"] = handle.ToInt64().ToString(CultureInfo.InvariantCulture);
                return result;
            }
            case "maximize":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                result["changed"] = ShowWindow(handle, 3);
                result["handle"] = handle.ToInt64().ToString(CultureInfo.InvariantCulture);
                return result;
            }
            case "restore":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                result["changed"] = ShowWindow(handle, 9);
                result["handle"] = handle.ToInt64().ToString(CultureInfo.InvariantCulture);
                return result;
            }
            case "close":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                result["changed"] = PostMessage(handle, 0x0010, IntPtr.Zero, IntPtr.Zero);
                result["handle"] = handle.ToInt64().ToString(CultureInfo.InvariantCulture);
                return result;
            }
            case "move":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                RECT frame = FrameBounds(handle);
                result["changed"] = MoveWindow(
                    handle,
                    RequiredInt(request, "x"),
                    RequiredInt(request, "y"),
                    frame.Right - frame.Left,
                    frame.Bottom - frame.Top,
                    true);
                result["window"] = WindowDescriptor(handle);
                return result;
            }
            case "resize":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                RECT frame = FrameBounds(handle);
                result["changed"] = MoveWindow(
                    handle,
                    frame.Left,
                    frame.Top,
                    RequiredInt(request, "width"),
                    RequiredInt(request, "height"),
                    true);
                result["window"] = WindowDescriptor(handle);
                return result;
            }
            case "frame_bounds":
            {
                IntPtr handle = HandleValue(request, "handle", true);
                RECT frame = FrameBounds(handle);
                result["frame"] = RectObject(frame.Left, frame.Top, frame.Right - frame.Left, frame.Bottom - frame.Top);
                result["coordinate_space"] = "screen";
                result["geometry_hash"] = GeometryHash(CurrentVirtualDesktop());
                return result;
            }
            case "find":
            {
                string title = Str(request, "title");
                List<Dictionary<string, object>> matches = new List<Dictionary<string, object>>();
                foreach (IntPtr handle in TopLevelWindows())
                {
                    string windowTitle = WindowTitle(handle);
                    if (string.IsNullOrWhiteSpace(windowTitle))
                    {
                        continue;
                    }
                    if (!string.IsNullOrEmpty(title) && windowTitle.IndexOf(title, StringComparison.OrdinalIgnoreCase) < 0)
                    {
                        continue;
                    }
                    matches.Add(WindowDescriptor(handle));
                }
                result["windows"] = matches;
                result["count"] = matches.Count;
                return result;
            }
            default:
                throw new ArgumentException("unsupported window action: " + action);
        }
    }

    private static Dictionary<string, object> HandleClipboard(Dictionary<string, object> request)
    {
        string action = (Str(request, "action") ?? string.Empty).Trim().ToLowerInvariant();
        Dictionary<string, object> result = new Dictionary<string, object>();
        switch (action)
        {
            case "get":
                result["text"] = Clipboard.ContainsText() ? Clipboard.GetText() : string.Empty;
                return result;
            case "set":
            {
                string text = Str(request, "text") ?? string.Empty;
                Clipboard.SetText(text);
                result["changed"] = true;
                result["length"] = text.Length;
                return result;
            }
            case "clear":
                Clipboard.Clear();
                result["changed"] = true;
                return result;
            default:
                throw new ArgumentException("unsupported clipboard action: " + action);
        }
    }

    // ---------------------------------------------------------------------
    // Entry point
    // ---------------------------------------------------------------------

    private static void WriteResult(Dictionary<string, object> payload)
    {
        if (!payload.ContainsKey("ok"))
        {
            payload["ok"] = true;
        }
        payload["platform"] = "windows";
        payload["bridge"] = BridgeVersion;
        Console.Out.WriteLine(ResultPrefix + Json.Serialize(payload));
        Console.Out.Flush();
    }

    private static void WriteError(string code, string message)
    {
        Dictionary<string, object> payload = new Dictionary<string, object>();
        payload["ok"] = false;
        payload["code"] = code;
        payload["error"] = message;
        WriteResult(payload);
    }

    [STAThread]
    private static int Main(string[] args)
    {
        try
        {
            Console.OutputEncoding = Encoding.UTF8;
        }
        catch (Exception)
        {
        }
        EnableDpiAwareness();

        if (args != null && args.Length > 0)
        {
            string first = args[0].Trim().ToLowerInvariant();
            if (first == "--version")
            {
                Console.Out.WriteLine(BridgeVersion);
                return 0;
            }
            if (first == "--self-test" || first == "--probe")
            {
                Dictionary<string, object> probe = new Dictionary<string, object>();
                VirtualDesktop vd = CurrentVirtualDesktop();
                POINT cursor = CursorPosition();
                probe["ok"] = true;
                probe["width"] = vd.Width;
                probe["height"] = vd.Height;
                probe["origin"] = PointObject(vd.X, vd.Y);
                probe["cursor"] = PointObject(cursor.X, cursor.Y);
                probe["monitors"] = EnumerateMonitors();
                probe["geometry_hash"] = GeometryHash(vd);
                probe["uia_available"] = AutomationElement.RootElement != null;
                WriteResult(probe);
                return 0;
            }
        }

        string raw;
        try
        {
            raw = Console.In.ReadToEnd();
        }
        catch (Exception exception)
        {
            WriteError("input_error", exception.Message);
            return 0;
        }
        if (string.IsNullOrWhiteSpace(raw))
        {
            WriteError("bad_request", "request is empty");
            return 0;
        }

        Dictionary<string, object> request;
        try
        {
            request = Json.Deserialize<Dictionary<string, object>>(raw);
        }
        catch (Exception exception)
        {
            WriteError("bad_request", "invalid JSON request: " + exception.Message);
            return 0;
        }
        if (request == null)
        {
            WriteError("bad_request", "invalid JSON request: empty object");
            return 0;
        }

        try
        {
            Dictionary<string, object> stale = new Dictionary<string, object>();
            if (!GeometryMatches(request, stale))
            {
                WriteResult(stale);
                return 0;
            }

            string tool = (Str(request, "tool") ?? string.Empty).Trim().ToLowerInvariant();
            Dictionary<string, object> result;
            switch (tool)
            {
                case "computer_observe":
                    result = HandleObserve(request);
                    break;
                case "computer_pointer":
                    result = HandlePointer(request);
                    break;
                case "computer_keyboard":
                    result = HandleKeyboard(request);
                    break;
                case "computer_window":
                    result = HandleWindow(request);
                    break;
                case "computer_clipboard":
                    result = HandleClipboard(request);
                    break;
                default:
                    WriteError("unsupported_tool", "unsupported computer tool: " + tool);
                    return 0;
            }
            WriteResult(result);
        }
        catch (Exception exception)
        {
            WriteError("execution_error", exception.GetType().Name + ": " + exception.Message);
        }
        return 0;
    }
}
