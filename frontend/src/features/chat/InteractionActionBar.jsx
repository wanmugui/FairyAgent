import React, { useState, useRef, useEffect } from "react";
import { extractAssistantContent } from "../../api/chat";
import { cleanTTSText } from "../../utils/ttsText";

let replayCtx = null;
function getReplayContext() {
  if (!replayCtx) replayCtx = new (window.AudioContext || window.webkitAudioContext)();
  if (replayCtx.state === "suspended") replayCtx.resume();
  return replayCtx;
}

function CopyIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3"/></svg>;
}

function RefreshIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0 2 5.3"/><path d="M20 4v7h-7"/></svg>;
}

function TrashIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="m19 6-1 14H6L5 6"/><path d="M10 11v5M14 11v5"/></svg>;
}

function VolumeIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M11 5 6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>;
}

function ReplayActionButton({ audio, text }) {
  const [phase, setPhase] = useState("idle");
  const sourceRef = useRef(null);
  const abortRef = useRef(null);
  const stop = () => {
    if (abortRef.current) abortRef.current.abort();
    abortRef.current = null;
    if (sourceRef.current) { try { sourceRef.current.stop(); } catch {} sourceRef.current = null; }
    setPhase("idle");
  };
  useEffect(() => stop, []);
  const playDecoded = async (decode) => {
    const ctx = getReplayContext();
    const buffer = await decode(ctx);
    if (!buffer) return;
    const source = ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(ctx.destination);
    source.onended = () => { if (sourceRef.current === source) { sourceRef.current = null; setPhase("idle"); } };
    sourceRef.current = source;
    setPhase("playing");
    source.start();
  };
  const play = async () => {
    if (phase !== "idle") { stop(); return; }
    try {
      if (audio && audio.url) {
        await playDecoded(async (ctx) => {
          const response = await fetch(audio.url);
          if (!response.ok) return null;
          return ctx.decodeAudioData(await response.arrayBuffer());
        });
        return;
      }
      if (!text) return;
      setPhase("loading");
      const controller = new AbortController();
      abortRef.current = controller;
      const response = await fetch("/voice/api/tts", {
        method: "POST",
        headers: { "Content-Type": "application/json; charset=utf-8" },
        body: JSON.stringify({ text }),
        signal: controller.signal,
      });
      if (!response.ok) throw new Error("tts failed");
      const data = await response.json();
      if (!data.audio_b64) throw new Error("tts returned no audio");
      await playDecoded(async (ctx) => {
        const raw = Uint8Array.from(atob(data.audio_b64), ch => ch.charCodeAt(0));
        const samples = new Float32Array(raw.buffer);
        const frames = Math.floor(samples.length / 2);
        const buffer = ctx.createBuffer(2, frames, data.sample_rate || 48000);
        for (let channel = 0; channel < 2; channel++) {
          const target = buffer.getChannelData(channel);
          for (let i = 0; i < frames; i++) target[i] = samples[i * 2 + channel];
        }
        return buffer;
      });
    } catch (error) {
      if (error && error.name !== "AbortError") stop();
    } finally {
      abortRef.current = null;
    }
  };
  const label = phase === "loading" ? "生成中" : phase === "playing" ? "停止" : "朗读";
  const title = phase === "loading" ? "正在生成语音，点击取消" : phase === "playing" ? "正在朗读，点击停止" : "朗读本轮回复";
  return (
    <button type="button" className={"interaction-action-button" + (phase === "playing" ? " is-playing" : "")} onClick={play} title={title}>
      <VolumeIcon />
      <span>{label}</span>
    </button>
  );
}

async function copyText(value) {
  const text = String(value || "");
  if (!text) return false;
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    let copied = false;
    try { copied = document.execCommand("copy"); } catch {}
    document.body.removeChild(area);
    return copied;
  }
}

export default function InteractionActionBar({
  userMessage,
  assistantMessage,
  interactionId,
  turnId,
  sessionName,
  audio,
  onDeleteInteraction,
  onRegenerate,
}) {
  const [copied, setCopied] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [regenerating, setRegenerating] = useState(false);
  const reply = assistantMessage ? extractAssistantContent(String(assistantMessage.content || "")).trim() : "";
  const speech = cleanTTSText(reply);
  const canDelete = Boolean(interactionId && onDeleteInteraction);
  const canRegenerate = Boolean(userMessage && interactionId && onRegenerate && !regenerating);
  const canRead = Boolean(speech || audio);

  if (!reply && !canDelete && !canRegenerate && !audio) return null;

  const copy = async () => {
    if (await copyText(reply)) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    }
  };

  const remove = (scope) => {
    setDeleteOpen(false);
    if (scope === "conversation+memory" && !window.confirm("删除这一轮对话和对应记忆？附件与生成结果不会被删除。")) return;
    Promise.resolve(onDeleteInteraction(interactionId, scope, { turnId, session: sessionName })).catch(() => {});
  };

  const regenerate = async () => {
    if (!canRegenerate) return;
    setRegenerating(true);
    try {
      await onRegenerate({ userMessage, interactionId, turnId, sessionName });
    } finally {
      setRegenerating(false);
    }
  };

  return (
    <div className="interaction-action-bar" role="toolbar" aria-label="本轮操作">
      {reply && (
        <button type="button" className="interaction-action-button" onClick={copy} title="复制回复">
          <CopyIcon />
          <span>{copied ? "已复制" : "复制"}</span>
        </button>
      )}
      {canRegenerate && (
        <button type="button" className="interaction-action-button" onClick={regenerate} disabled={regenerating} title="重新生成这一轮回复">
          <RefreshIcon />
          <span>{regenerating ? "生成中" : "重新生成"}</span>
        </button>
      )}
      {canDelete && (
        <div className="interaction-delete-control">
          <button type="button" className="interaction-action-button" onClick={() => setDeleteOpen(value => !value)} aria-expanded={deleteOpen} title="删除本轮内容">
            <TrashIcon />
            <span>删除</span>
          </button>
          {deleteOpen && (
            <div className="interaction-action-menu" role="menu">
              <button type="button" role="menuitem" onClick={() => remove("memory")}>仅删除本轮记忆</button>
              <button type="button" role="menuitem" className="danger" onClick={() => remove("conversation+memory")}>删除对话和记忆</button>
              <span>附件与产物保留</span>
            </div>
          )}
        </div>
      )}
      {canRead && <ReplayActionButton audio={audio} text={speech} />}
    </div>
  );
}
