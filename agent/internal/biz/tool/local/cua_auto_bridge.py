import json
import os
import platform
import sys


RESULT_PREFIX = "__FAIRY_CUA_RESULT__"


def emit(payload):
    payload.setdefault("ok", True)
    print(RESULT_PREFIX + json.dumps(payload, ensure_ascii=False), flush=True)


def as_int(value, field):
    if value is None:
        raise ValueError(f"{field} is required")
    return int(value)


def optional_int(value):
    if value is None or value == "":
        return None
    return int(value)


def as_text(value, field):
    if value is None or str(value) == "":
        raise ValueError(f"{field} is required")
    return str(value)


def observe(request):
    import cua_auto.screen as screen
    import cua_auto.window as window

    action = request["action"]
    if action == "screen_info":
        width, height = screen.screen_size()
        x, y = screen.cursor_position()
        return {
            "width": int(width),
            "height": int(height),
            "cursor": {"x": int(x), "y": int(y)},
            "display_scale": float(screen.get_display_scale()),
        }
    if action == "cursor_position":
        x, y = screen.cursor_position()
        return {"x": int(x), "y": int(y)}
    if action == "screenshot":
        output_path = as_text(request.get("output_path"), "output_path")
        os.makedirs(os.path.dirname(os.path.abspath(output_path)), exist_ok=True)
        image = screen.screenshot()
        image.save(output_path, format="PNG")
        return {
            "path": os.path.abspath(output_path),
            "width": int(image.width),
            "height": int(image.height),
            "size": int(os.path.getsize(output_path)),
        }
    if action == "active_window":
        handle = window.get_active_window_handle()
        result = {
            "title": window.get_active_window_title(),
            "handle": handle,
        }
        if handle is not None:
            position = window.get_window_position(handle)
            size = window.get_window_size(handle)
            if position:
                result["x"], result["y"] = int(position[0]), int(position[1])
            if size:
                result["width"], result["height"] = int(size[0]), int(size[1])
        return result
    if action == "list_windows":
        import pywinctl as pwc

        title_filter = str(request.get("title") or "").strip().lower()
        active_handle = window.get_active_window_handle()
        windows = []
        for item in pwc.getAllWindows():
            try:
                title = str(item.title or "")
                if title_filter and title_filter not in title.lower():
                    continue
                handle = str(item.getHandle())
                entry = {
                    "handle": handle,
                    "title": title,
                    "active": handle == active_handle,
                }
                try:
                    x, y = item.position
                    entry["x"], entry["y"] = int(x), int(y)
                except Exception:
                    pass
                try:
                    width, height = item.size
                    entry["width"], entry["height"] = int(width), int(height)
                except Exception:
                    pass
                windows.append(entry)
            except Exception:
                continue
        return {"windows": windows, "count": len(windows)}
    raise ValueError(f"unsupported observe action: {action}")


def pointer(request):
    import cua_auto.mouse as mouse

    action = request["action"]
    button = str(request.get("button") or "left")
    if action == "move":
        mouse.move_to(as_int(request.get("x"), "x"), as_int(request.get("y"), "y"))
    elif action == "click":
        mouse.click(as_int(request.get("x"), "x"), as_int(request.get("y"), "y"), button)
    elif action == "double_click":
        mouse.double_click(as_int(request.get("x"), "x"), as_int(request.get("y"), "y"))
    elif action == "right_click":
        mouse.right_click(as_int(request.get("x"), "x"), as_int(request.get("y"), "y"))
    elif action == "drag":
        mouse.drag(
            as_int(request.get("start_x"), "start_x"),
            as_int(request.get("start_y"), "start_y"),
            as_int(request.get("end_x"), "end_x"),
            as_int(request.get("end_y"), "end_y"),
            button,
        )
    elif action == "scroll":
        mouse.scroll(int(request.get("dx") or 0), int(request.get("dy") or 0))
    elif action == "mouse_down":
        mouse.mouse_down(optional_int(request.get("x")), optional_int(request.get("y")), button)
    elif action == "mouse_up":
        mouse.mouse_up(optional_int(request.get("x")), optional_int(request.get("y")), button)
    else:
        raise ValueError(f"unsupported pointer action: {action}")
    return {"action": action, "button": button}


def keyboard(request):
    import cua_auto.keyboard as keyboard_control

    action = request["action"]
    if action == "type":
        keyboard_control.type_text(str(request.get("text") or ""))
    elif action == "press":
        keyboard_control.press_key(as_text(request.get("key"), "key"))
    elif action == "hotkey":
        keys = request.get("keys")
        if not isinstance(keys, list) or not keys:
            raise ValueError("keys must be a non-empty array")
        keyboard_control.hotkey([str(key) for key in keys])
    elif action == "key_down":
        keyboard_control.key_down(as_text(request.get("key"), "key"))
    elif action == "key_up":
        keyboard_control.key_up(as_text(request.get("key"), "key"))
    else:
        raise ValueError(f"unsupported keyboard action: {action}")
    return {"action": action}


def window_control(request):
    import cua_auto.window as window

    action = request["action"]
    if action == "open":
        target = as_text(request.get("target"), "target")
        return {"changed": bool(window.open(target)), "target": target}

    handle = as_text(request.get("handle"), "handle")
    if action == "activate":
        changed = window.activate_window(handle)
    elif action == "minimize":
        changed = window.minimize_window(handle)
    elif action == "maximize":
        changed = window.maximize_window(handle)
    elif action == "close":
        changed = window.close_window(handle)
    elif action == "move":
        changed = window.set_window_position(
            handle,
            as_int(request.get("x"), "x"),
            as_int(request.get("y"), "y"),
        )
    elif action == "resize":
        changed = window.set_window_size(
            handle,
            as_int(request.get("width"), "width"),
            as_int(request.get("height"), "height"),
        )
    else:
        raise ValueError(f"unsupported window action: {action}")
    return {"changed": bool(changed), "handle": handle, "action": action}


def clipboard(request):
    import cua_auto.clipboard as clipboard_control

    action = request["action"]
    if action == "get":
        return {"text": clipboard_control.get()}
    if action == "set":
        clipboard_control.set(str(request.get("text") or ""))
        return {"changed": True}
    raise ValueError(f"unsupported clipboard action: {action}")


def dispatch(request):
    tool = request.get("tool")
    handlers = {
        "computer_observe": observe,
        "computer_pointer": pointer,
        "computer_keyboard": keyboard,
        "computer_window": window_control,
        "computer_clipboard": clipboard,
    }
    handler = handlers.get(tool)
    if handler is None:
        raise ValueError(f"unsupported computer tool: {tool}")
    result = handler(request)
    result["platform"] = platform.system().lower()
    return result


def main():
    try:
        request = json.load(sys.stdin)
        if not isinstance(request, dict):
            raise ValueError("request must be a JSON object")
        emit(dispatch(request))
    except Exception as exc:
        emit({
            "ok": False,
            "code": "execution_error",
            "error": f"{type(exc).__name__}: {exc}",
        })


if __name__ == "__main__":
    main()
