import React, { useState } from 'react';

const modes = [
  { value: 'no-template', label: '无模板模式', description: '由 AI 自由设计简洁的商务或学术版式' },
  { value: 'creative', label: '创意模式', description: '使用更具视觉设计感的创意排版' },
];

const initialConfig = {
  role: '演讲者',
  scene: '通用演示',
  audience: '普通受众',
  page_count_desc: '10 页左右',
  ppt_mode: 'no-template',
};

// The production UI returns a structured PPT parameter payload for this ask
// type. Keep the local harness equally explicit instead of turning the reply
// into an ordinary natural-language answer that the PPT Skill cannot consume.
export default function PPTConfigModal({ onComplete, onSkipAll }) {
  const [config, setConfig] = useState(initialConfig);
  const update = (key, value) => setConfig(prev => ({ ...prev, [key]: value }));

  return (
    <div className="ask-modal-overlay">
      <div className="ask-modal-card ppt-config-modal">
        <div className="ask-modal-header">
          <div className="ask-modal-title">确认 PPT 制作参数</div>
          <div className="ppt-config-hint">这些参数只用于当前这份演示文稿。</div>
        </div>

        <div className="ppt-mode-options">
          {modes.map(mode => (
            <label className={`ppt-mode-option ${config.ppt_mode === mode.value ? 'selected' : ''}`} key={mode.value}>
              <input
                type="radio"
                name="ppt-mode"
                value={mode.value}
                checked={config.ppt_mode === mode.value}
                onChange={event => update('ppt_mode', event.target.value)}
              />
              <span><strong>{mode.label}</strong><small>{mode.description}</small></span>
            </label>
          ))}
        </div>

        <div className="ppt-config-fields">
          <label>演讲者身份<input value={config.role} onChange={event => update('role', event.target.value)} /></label>
          <label>使用场景<input value={config.scene} onChange={event => update('scene', event.target.value)} /></label>
          <label>目标受众<input value={config.audience} onChange={event => update('audience', event.target.value)} /></label>
          <label>页数<input value={config.page_count_desc} onChange={event => update('page_count_desc', event.target.value)} /></label>
        </div>

        <div className="ask-modal-footer">
          <span className="ask-modal-skip" onClick={onSkipAll}>取消</span>
          <button className="ask-modal-next" onClick={() => onComplete({ pptConfig: config })}>确认并开始</button>
        </div>
      </div>
    </div>
  );
}
