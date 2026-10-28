import React, { useEffect, useMemo, useRef, useState } from 'react'

// VoiceWaveform — the animated level bar strip borrowed from DSH's
// dsh-fairy-voice-wave-form, redrawn so it stands alone in the Tailwind
// theme. CSS variables are local to this component (no DSH token deps).
//
// Visual model: 30 thin vertical bars in a horizontal row. Each bar's height
// is a fixed "tall" baseline (e.g. 14px) at idle, then modulated by the
// current audio level (0..1) when active. The active subset grows from
// the left as level rises; the rest stay at low opacity.
//
// Props:
//   active      — when true, the component animates to the supplied level.
//   level       — 0..1 audio level (RMS normalised). Defaults to 0.
//   barCount    — number of bars to render. Defaults to 30.
//   height      — px for full-active bar. Defaults to 14.
//   color       — CSS color for active bars. Defaults to currentColor.
//   className   — extra class to add to the wrapper.

const DEFAULT_BAR_COUNT = 30
const DEFAULT_HEIGHT = 14

export default function VoiceWaveform({
  active = false,
  level = 0,
  barCount = DEFAULT_BAR_COUNT,
  height = DEFAULT_HEIGHT,
  color = 'currentColor',
  className = '',
}) {
  // Pin random per-bar base heights so the strip feels organic instead of
  // symmetric. Recompute only if barCount or height changes.
  const baseHeights = useMemo(() => {
    const out = new Array(barCount)
    for (let i = 0; i < barCount; i++) {
      // Mirror the DSH bias: shorter at the ends, taller in the middle.
      const t = (i / (barCount - 1)) * 2 - 1
      const base = 0.45 + 0.55 * (1 - t * t)
      out[i] = Math.max(0.35, Math.min(1, base + (Math.sin(i * 1.3) * 0.07)))
    }
    return out
  }, [barCount])

  // Smooth level for a less jittery bar height. We don't need a real
  // envelope follower; a single-pole IIR with alpha=0.4 is enough.
  const smoothRef = useRef(0)
  const [, force] = useState(0)
  useEffect(() => {
    if (!active) {
      smoothRef.current = 0
      force((x) => x + 1)
      return
    }
    let id
    const tick = () => {
      const prev = smoothRef.current
      const next = prev + 0.4 * (Math.max(0, Math.min(1, level)) - prev)
      if (Math.abs(next - prev) > 0.005) {
        smoothRef.current = next
        force((x) => x + 1)
      }
      id = requestAnimationFrame(tick)
    }
    id = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(id)
  }, [active, level])

  // How many bars are "filled" at the current smoothed level.
  const smooth = smoothRef.current
  const filled = Math.round(smooth * barCount)

  return (
    <span
      className={`voice-waveform ${className}`.trim()}
      role="group"
      aria-hidden="true"
      data-active={active ? 'true' : 'false'}
      data-level={smooth.toFixed(3)}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 3,
        height,
        color,
      }}
    >
      {baseHeights.map((h, i) => {
        const isOn = active && i < filled
        return (
          <span
            key={i}
            className="voice-waveform-bar"
            data-on={isOn ? 'true' : 'false'}
            style={{
              width: 3,
              minWidth: 3,
              height: isOn ? Math.max(3, h * height) : 3,
              borderRadius: 999,
              background: 'currentColor',
              opacity: isOn ? 0.9 : 0.22,
              transition: 'height 80ms ease, opacity 80ms ease',
            }}
          />
        )
      })}
    </span>
  )
}
