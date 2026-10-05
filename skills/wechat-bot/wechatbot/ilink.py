"""微信官方 iLink（机器人）接口客户端。私聊：收发文本，发图片、视频、文件。

发图这条链路没有公开文档，照着开源实现（photon-hq/wechat-ilink-client）
对出来的三步：

  1. `ilink/bot/getuploadurl`：带上 filekey / media_type / to_user_id / rawsize /
     rawfilemd5 / filesize / no_need_thumb / aeskey，换回一个 `upload_param`
  2. 把文件用 AES-128-ECB（PKCS7）加密后 POST 到微信 CDN：
     `{cdn}/upload?encrypted_query_param=<upload_param>&filekey=<filekey>`，
     响应头 `x-encrypted-param` 就是之后要填进 item 的下载参数
  3. `sendmessage` 里放 `image_item.media{encrypt_query_param, aes_key(base64),
     encrypt_type:1}` 和 `mid_size`（密文长度）

早先"用 multipart 直接传文件"或"URL 直发"都会拿到 Platform 那套空壳（客户端
显示成"已过期"），因为平台要的从来不是原始文件，而是这套加密引用。
"""
from __future__ import annotations

import base64
import hashlib
import io
import json
import logging
import pathlib
import random
import secrets
import shutil
import subprocess
import uuid
from urllib.parse import quote

import requests

log = logging.getLogger("wechat-ilink")

DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com"
# 微信 CDN（媒体上传/下载都走它，不在 api 域名下）
CDN_BASE_URL = "https://novac2c.cdn.weixin.qq.com/c2c"

# 上传接口的 media_type：1 图 2 视频 3 文件 4 语音
MEDIA_IMAGE = 1
MEDIA_VIDEO = 2
MEDIA_FILE = 3
MEDIA_VOICE = 4

# 消息 item 的 type 是**另一套编号**，别混用：正文是 1，图片是 2。
# （踩过的坑：把 image_item 装进 type=1 的 item 里，平台当成一条空文本，
# 对方就只看到文字、看不到图。）
ITEM_TEXT = 1
ITEM_IMAGE = 2
ITEM_VOICE = 3
ITEM_FILE = 4
ITEM_VIDEO = 5


def aes_ecb_padded_size(size: int) -> int:
    """AES-128-ECB + PKCS7 之后的长度（PKCS7 至少补 1 字节）。"""
    return ((int(size) + 1 + 15) // 16) * 16


def encrypt_aes_ecb(plaintext: bytes, key: bytes) -> bytes:
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

    padding = 16 - (len(plaintext) % 16)
    padded = plaintext + bytes([padding]) * padding
    encryptor = Cipher(algorithms.AES(key), modes.ECB()).encryptor()
    return encryptor.update(padded) + encryptor.finalize()


def decrypt_aes_ecb(ciphertext: bytes, key: bytes) -> bytes:
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

    decryptor = Cipher(algorithms.AES(key), modes.ECB()).decryptor()
    plain = decryptor.update(ciphertext) + decryptor.finalize()
    return plain[: -plain[-1]]


class ILinkError(RuntimeError):
    pass


class ILinkClient:
    def __init__(self, base_url: str = DEFAULT_BASE_URL, token: str | None = None) -> None:
        self.base_url = (base_url or DEFAULT_BASE_URL).rstrip("/")
        self.token = token

    def _headers(self, token_required: bool = False) -> dict:
        headers = {
            "Content-Type": "application/json",
            "AuthorizationType": "ilink_bot_token",
            "iLink-App-ClientVersion": "1",
            "X-WECHAT-UIN": base64.b64encode(str(random.getrandbits(32)).encode()).decode(),
        }
        if token_required and self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    def request_json(self, method: str, endpoint: str, *, params=None, payload=None,
                     token_required: bool = False, timeout: int = 40) -> dict:
        url = f"{self.base_url}/{endpoint.lstrip('/')}"
        resp = requests.request(method, url, params=params, json=payload,
                                headers=self._headers(token_required), timeout=timeout)
        if resp.status_code >= 400:
            raise ILinkError(f"{method} {endpoint} -> HTTP {resp.status_code}: {resp.text[:200]}")
        if not resp.text:
            return {}
        try:
            return resp.json()
        except ValueError as exc:
            raise ILinkError(f"{method} {endpoint} 返回非 JSON: {resp.text[:200]}") from exc

    # ---- 登录 ----
    def new_qrcode(self, bot_type: str = "3") -> dict:
        return self.request_json("GET", "ilink/bot/get_bot_qrcode",
                                 params={"bot_type": bot_type}, timeout=30)

    def qrcode_status(self, qrcode: str) -> dict:
        return self.request_json("GET", "ilink/bot/get_qrcode_status",
                                 params={"qrcode": qrcode}, timeout=40)

    # ---- 收发 ----
    def get_updates(self, buf: str = "", timeout: int = 45) -> dict:
        return self.request_json(
            "POST", "ilink/bot/getupdates",
            payload={"base_info": {"channel_version": "fairy"}, "get_updates_buf": buf},
            token_required=True, timeout=timeout)

    def send_text(self, user_id: str, text: str, context_token: str) -> dict:
        return self.send_items(user_id, context_token, [{"type": ITEM_TEXT, "text_item": {"text": text}}])

    def send_items(self, user_id: str, context_token: str, items: list[dict]) -> dict:
        """发一条 bot 消息。context_token 是"对方最近跟这个 bot 说过话"的凭据。"""
        if not context_token:
            raise ILinkError("缺少 context_token：需要先收到对方的一条消息")
        return self.request_json(
            "POST", "ilink/bot/sendmessage",
            payload={
                "base_info": {"channel_version": "fairy"},
                "msg": {
                    "from_user_id": "",
                    "to_user_id": user_id,
                    "client_id": uuid.uuid4().hex,
                    "message_type": 2,
                    "message_state": 2,
                    "context_token": context_token,
                    "item_list": items,
                },
            },
            token_required=True, timeout=30)

    # ---- 富媒体 -----------------------------------------------------------

    def get_upload_url(self, *, to_user_id: str, filekey: str, media_type: int, rawsize: int,
                       rawfilemd5: str, filesize: int, aeskey_hex: str,
                       thumb: dict | None = None) -> dict:
        """向平台申请一个上传名额，换回 upload_param。"""
        payload = {
            "base_info": {"channel_version": "fairy"},
            "filekey": filekey,
            "media_type": int(media_type),
            "to_user_id": to_user_id,
            "rawsize": int(rawsize),
            "rawfilemd5": rawfilemd5,
            "filesize": int(filesize),
            "no_need_thumb": not thumb,
            "aeskey": aeskey_hex,
        }
        if thumb:
            payload.update({
                "thumb_rawsize": int(thumb["rawsize"]),
                "thumb_rawfilemd5": str(thumb["rawfilemd5"]),
                "thumb_filesize": int(thumb["filesize"]),
            })
        return self.request_json("POST", "ilink/bot/getuploadurl", payload=payload,
                                 token_required=True, timeout=30)

    def upload_media(self, to_user_id: str, data: bytes, media_type: int = MEDIA_IMAGE,
                     thumb: bytes | None = None) -> dict:
        """把一段字节传上微信 CDN，返回可填进 item 的字段。

        缩略图不是可选项而是"能不能看见"的关键：不带 thumb 时平台发出去的气泡
        是空白的（发送成功、对方什么都看不到），这正是"只有文字没有图片"的样子。
        缩略图和主文件共用同一个 aeskey（协议就是这样：一次 getuploadurl 发两个
        上传地址）。
        """
        rawsize = len(data)
        rawfilemd5 = hashlib.md5(data).hexdigest()
        filesize = aes_ecb_padded_size(rawsize)
        filekey = secrets.token_hex(16)
        aeskey = secrets.token_bytes(16)
        thumb_request = None
        if thumb:
            thumb_request = {
                "rawsize": len(thumb),
                "rawfilemd5": hashlib.md5(thumb).hexdigest(),
                "filesize": aes_ecb_padded_size(len(thumb)),
            }

        answer = self.get_upload_url(
            to_user_id=to_user_id, filekey=filekey, media_type=media_type,
            rawsize=rawsize, rawfilemd5=rawfilemd5, filesize=filesize,
            aeskey_hex=aeskey.hex(),
            thumb=thumb_request,
        )
        # 这个版本的接口直接把整条上传地址给回来（带 encrypted_query_param/filekey/
        # taskid）；老的实现里是只给 upload_param 由客户端自己拼，两种都兜住。
        url = str(answer.get("upload_full_url") or "").strip()
        if not url:
            upload_param = str(answer.get("upload_param") or "").strip()
            if not upload_param:
                raise ILinkError("getuploadurl 既没有 upload_full_url 也没有 upload_param：" + str(answer)[:200])
            url = (f"{CDN_BASE_URL}/upload?encrypted_query_param={quote(upload_param)}"
                   f"&filekey={quote(filekey)}")
        response = requests.post(
            url,
            data=encrypt_aes_ecb(data, aeskey),
            headers={"Content-Type": "application/octet-stream"},
            timeout=90,
        )
        if response.status_code >= 400:
            raise ILinkError(f"CDN 上传失败 HTTP {response.status_code}: {response.text[:200]}")
        download_param = str(response.headers.get("x-encrypted-param") or "").strip()
        if not download_param:
            raise ILinkError("CDN 上传成功但没有 x-encrypted-param 响应头")
        media = {
            "encrypt_query_param": download_param,
            "aes_key": base64.b64encode(aeskey).decode("ascii"),
            "aeskey_hex": aeskey.hex(),
            "mid_size": filesize,
            "filekey": filekey,
        }
        if thumb_request:
            thumb_url = str(answer.get("thumb_upload_full_url") or "").strip()
            if not thumb_url:
                thumb_param = str(answer.get("thumb_upload_param") or "").strip()
                thumb_url = (f"{CDN_BASE_URL}/upload?encrypted_query_param={quote(thumb_param)}"
                             f"&filekey={quote(filekey)}") if thumb_param else ""
            if thumb_url:
                thumb_response = requests.post(
                    thumb_url,
                    data=encrypt_aes_ecb(thumb, aeskey),
                    headers={"Content-Type": "application/octet-stream"},
                    timeout=90,
                )
                thumb_download_param = str(thumb_response.headers.get("x-encrypted-param") or "").strip()
                if thumb_response.status_code < 400 and thumb_download_param:
                    media["thumb_encrypt_query_param"] = thumb_download_param
                    media["thumb_mid_size"] = thumb_request["filesize"]
                else:
                    # 缩略图失败不该拖垮整条消息：没有它顶多是对方看不到预览。
                    log.warning("缩略图上传失败 HTTP %s", thumb_response.status_code)
        return media

    def download_media(self, encrypt_query_param: str, aeskey_hex: str) -> bytes:
        """按微信 CDN 的规矩下载并解密一条媒体（图片的 aeskey 在 image_item.aeskey）。"""
        if not encrypt_query_param or not aeskey_hex:
            raise ILinkError("下载媒体需要 encrypt_query_param 和 aeskey")
        url = (f"{CDN_BASE_URL}/download?"
               f"encrypted_query_param={quote(str(encrypt_query_param), safe='')}")
        response = requests.get(url, timeout=90)
        if response.status_code != 200:
            raise ILinkError(f"CDN 下载失败 HTTP {response.status_code}: {response.text[:200]}")
        return decrypt_aes_ecb(response.content, bytes.fromhex(str(aeskey_hex)))

    def send_image(self, user_id: str, image: str | pathlib.Path, context_token: str,
                   caption: str = "") -> dict:
        """发一张本地图片（可选先发一句说明）。"""
        data = pathlib.Path(image).expanduser().read_bytes()
        thumb, thumb_size = make_thumbnail(data)
        media = self.upload_media(user_id, data, MEDIA_IMAGE, thumb=thumb)
        # 字段形状照抄平台自己发来的那条图片消息（image_item.aeskey 是 hex，
        # media.aes_key 是"那串 hex 再 base64"）。之前只填 media.aes_key=base64
        # (原始 16 字节)，客户端认不出来，于是渲染成空壳。
        aes_key_field = base64.b64encode(media["aeskey_hex"].encode("ascii")).decode("ascii")
        item = {
            "type": ITEM_IMAGE,
            "image_item": {
                "aeskey": media["aeskey_hex"],
                "media": {
                    "encrypt_query_param": media["encrypt_query_param"],
                    "aes_key": aes_key_field,
                    "encrypt_type": 1,
                },
                "mid_size": media["mid_size"],
            },
        }
        if media.get("thumb_encrypt_query_param"):
            item["image_item"]["thumb_media"] = {
                "encrypt_query_param": media["thumb_encrypt_query_param"],
                "aes_key": aes_key_field,
                "encrypt_type": 1,
            }
            item["image_item"]["thumb_size"] = media["thumb_mid_size"]
            if thumb_size:
                item["image_item"]["thumb_width"], item["image_item"]["thumb_height"] = thumb_size
        if caption:
            self.send_items(user_id, context_token, [{"type": ITEM_TEXT, "text_item": {"text": caption}}])
        return self.send_items(user_id, context_token, [item])

    def send_video(self, user_id: str, video: str | pathlib.Path, context_token: str,
                   caption: str = "") -> dict:
        """发一个本地视频文件（可选先发一句说明）。

        字段形状取自 photon-hq/wechat-ilink-client 的 VideoItem：
        media / video_size / play_length / video_md5 / thumb_media。
        注意 VideoItem 没有 ImageItem 那个顶层 aeskey，解密只认 media.aes_key，
        这里仍然把 media 构造成和图片同一种（aes_key = "hex 再 base64"）形状。
        缩略图同样必需：缺了对方只会看到一个空白气泡。
        """
        path = pathlib.Path(video).expanduser()
        data = path.read_bytes()
        thumb, thumb_size = make_video_thumbnail(path)
        media = self.upload_media(user_id, data, MEDIA_VIDEO, thumb=thumb)
        aes_key_field = base64.b64encode(media["aeskey_hex"].encode("ascii")).decode("ascii")
        duration, width, height = probe_video(path)
        video_item = {
            "media": {
                "encrypt_query_param": media["encrypt_query_param"],
                "aes_key": aes_key_field,
                "encrypt_type": 1,
            },
            "video_size": len(data),
            "video_md5": hashlib.md5(data).hexdigest(),
            "play_length": int(duration),
        }
        if width and height:
            video_item["width"], video_item["height"] = int(width), int(height)
        if media.get("thumb_encrypt_query_param"):
            video_item["thumb_media"] = {
                "encrypt_query_param": media["thumb_encrypt_query_param"],
                "aes_key": aes_key_field,
                "encrypt_type": 1,
            }
            video_item["thumb_size"] = media["thumb_mid_size"]
            if thumb_size:
                video_item["thumb_width"], video_item["thumb_height"] = thumb_size
        item = {"type": ITEM_VIDEO, "video_item": video_item}
        if caption:
            self.send_items(user_id, context_token, [{"type": ITEM_TEXT, "text_item": {"text": caption}}])
        return self.send_items(user_id, context_token, [item])

    def send_file(self, user_id: str, file: str | pathlib.Path, context_token: str,
                  caption: str = "") -> dict:
        """发一个任意本地文件（zip / pdf / 文档 / 压缩包）。

        字段形状取自 photon-hq/wechat-ilink-client 的 FileItem：
        media / file_name / md5 / len。注意 **len 是字符串而不是数字**，
        填成数字平台会当成非法 item 静默丢掉。FileItem 没有缩略图字段。
        """
        path = pathlib.Path(file).expanduser()
        data = path.read_bytes()
        media = self.upload_media(user_id, data, MEDIA_FILE)
        aes_key_field = base64.b64encode(media["aeskey_hex"].encode("ascii")).decode("ascii")
        item = {
            "type": ITEM_FILE,
            "file_item": {
                "media": {
                    "encrypt_query_param": media["encrypt_query_param"],
                    "aes_key": aes_key_field,
                    "encrypt_type": 1,
                },
                "file_name": path.name,
                "md5": hashlib.md5(data).hexdigest(),
                "len": str(len(data)),
            },
        }
        if caption:
            self.send_items(user_id, context_token, [{"type": ITEM_TEXT, "text_item": {"text": caption}}])
        return self.send_items(user_id, context_token, [item])

    @staticmethod
    def extract_text(message: dict) -> str:
        parts = []
        for item in (message or {}).get("item_list") or []:
            text = ((item or {}).get("text_item") or {}).get("text")
            if text:
                parts.append(str(text))
                continue
            # Inbound voice carries the server-side transcript (ASR) on
            # voice_item.text. Reading only text_item meant a spoken message
            # arrived as an empty turn and was dropped by the caller.
            voice_text = ((item or {}).get("voice_item") or {}).get("text")
            if voice_text:
                parts.append(str(voice_text))
        return "\n".join(parts).strip()


def make_thumbnail(data: bytes, max_side: int = 200) -> tuple[bytes | None, tuple[int, int] | None]:
    """给图片做一张小预览图（微信不带头像版气泡是空白的）。

    返回 (JPEG 字节, (宽, 高))；不是图片或处理失败时返回 (None, None)，让调用方
    退化成"只发主文件"。
    """
    try:
        from PIL import Image

        image = Image.open(io.BytesIO(data))
        image = image.convert("RGB") if image.mode not in ("RGB", "L") else image
        image.thumbnail((max_side, max_side))
        buffer = io.BytesIO()
        image.save(buffer, format="JPEG", quality=80)
        return buffer.getvalue(), image.size
    except Exception:  # noqa: BLE001
        log.debug("生成缩略图失败", exc_info=True)
        return None, None


def probe_video(path: pathlib.Path) -> tuple[float, int, int]:
    """读视频时长(秒)/宽/高。ffprobe 不可用或探测失败时返回 (0, 0, 0)。"""
    try:
        out = subprocess.run(
            [
                "ffprobe", "-v", "quiet", "-print_format", "json",
                "-show_format", "-show_streams", str(path),
            ],
            capture_output=True, text=True, timeout=30, check=False,
        )
        info = json.loads(out.stdout or "{}")
        duration = float((info.get("format") or {}).get("duration") or 0)
        for stream in info.get("streams") or []:
            if stream.get("codec_type") == "video":
                return duration, int(stream.get("width") or 0), int(stream.get("height") or 0)
        return duration, 0, 0
    except Exception:  # noqa: BLE001
        log.debug("ffprobe 探测视频失败", exc_info=True)
        return 0.0, 0, 0


def _ffmpeg_frame(path: pathlib.Path, at_second: float, max_side: int):
    """用 ffmpeg 抽一帧 JPEG 并缩放。成功返回 (bytes, (w, h))，失败 (None, None)。"""
    cmd = ["ffmpeg", "-v", "quiet", "-y"]
    if at_second > 0:
        cmd += ["-ss", str(at_second)]
    cmd += ["-i", str(path), "-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-"]
    out = subprocess.run(cmd, capture_output=True, timeout=60, check=False)
    if out.returncode != 0 or not out.stdout:
        return None, None
    return _shrink_jpeg(out.stdout, max_side)


def make_video_thumbnail(path: pathlib.Path, max_side: int = 200,
                         at_second: float = 1.0) -> tuple[bytes | None, tuple[int, int] | None]:
    """抽一帧做视频预览图（微信视频气泡没缩略图是空白的）。

    默认先试第 1 秒，很多视频第 0 秒是纯黑或片头；抽不到再退回第 0 帧。
    ffmpeg 不可用时返回 (None, None)，此时 send_video 退化成不带缩略图发送。
    """
    if shutil.which("ffmpeg") is None:
        log.debug("ffmpeg 不可用，视频将不带缩略图发送")
        return None, None
    for offset in (at_second, 0.0):
        try:
            thumb, size = _ffmpeg_frame(path, offset, max_side)
        except (OSError, subprocess.SubprocessError) as exc:
            log.debug("ffmpeg 抽帧失败 offset=%s", offset, exc_info=exc)
            return None, None
        if thumb:
            return thumb, size
    return None, None


def _shrink_jpeg(jpeg: bytes, max_side: int = 200) -> tuple[bytes | None, tuple[int, int] | None]:
    """把 ffmpeg 出的 JPEG 压到 max_side 以内，并返回 (宽, 高)。"""
    try:
        from PIL import Image

        image = Image.open(io.BytesIO(jpeg)).convert("RGB")
        image.thumbnail((max_side, max_side))
        buffer = io.BytesIO()
        image.save(buffer, format="JPEG", quality=80)
        return buffer.getvalue(), image.size
    except Exception:  # noqa: BLE001
        log.debug("缩放视频缩略图失败", exc_info=True)
        return None, None
