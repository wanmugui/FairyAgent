import React, { useCallback, useEffect, useState } from 'react';
import VoiceDock from '../features/voice/VoiceDock';
import { appBase } from '../utils/appBase';
import SettingsMenu from '../components/SettingsMenu';
import { fetchModels, fetchSettings, updateSettings } from '../api/chat';
import {
  MODEL_STORAGE_KEY,
  FALLBACK_ENABLED_STORAGE_KEY,
  FALLBACK_MODEL_STORAGE_KEY,
  pickValidFallbackModel,
  pickValidModel,
  readAutoFallback,
  readFallbackModel,
  readStoredModel,
  writeAutoFallback,
  writeFallbackModel,
  writeStoredModel,
} from '../utils/modelPreference';
import { readVoiceAutoRead, writeVoiceAutoRead } from '../utils/voicePreference';
import { voiceChannelForIdentity, voiceSessionName } from '../utils/voiceChannel';
import { currentTheme, saveThemePreference } from '../theme';

export default function VoicePage() {
  const [sessionName, setSessionName] = useState('');
  const [voiceChannel, setVoiceChannel] = useState(null);
  const sessionNameRef = React.useRef(sessionName);
  React.useEffect(() => { sessionNameRef.current = sessionName; }, [sessionName]);

  useEffect(() => {
    let cancelled = false;
    fetch('/auth/me', { cache: 'no-store' })
      .then(response => response.ok ? response.json() : null)
      .then(identity => {
        if (cancelled) return;
        const channel = voiceChannelForIdentity(identity);
        if (!channel) throw new Error('authenticated identity is required for voice sessions');
        setVoiceChannel(channel);
        setSessionName(voiceSessionName(channel));
      })
      .catch(error => {
        if (!cancelled) console.warn('[voice] failed to resolve voice channel:', error);
      });
    return () => { cancelled = true; };
  }, []);

  const [models, setModels] = useState([]);
  const [model, setModel] = useState(() => readStoredModel() || 'minimax-m3');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [theme, setTheme] = useState(() => currentTheme());
  const [autoFallback, setAutoFallback] = useState(() => readAutoFallback());
  const [fallbackModel, setFallbackModel] = useState(() => readFallbackModel());
  const [voiceAutoRead, setVoiceAutoRead] = useState(() => readVoiceAutoRead('voice'));

  const handleModelChange = useCallback((modelId) => {
    if (!modelId) return;
    setModel(modelId);
    writeStoredModel(modelId);
    const patch = { default_model: modelId };
    if (fallbackModel === modelId) {
      setFallbackModel('');
      writeFallbackModel('');
      patch.fallback_model = '';
    }
    updateSettings(patch).catch(() => {});
  }, [fallbackModel]);

  const handleModelsChanged = useCallback((list) => {
    const nextModels = Array.isArray(list) ? list : [];
    setModels(nextModels);
    setModel(prev => {
      const next = pickValidModel(nextModels, prev);
      if (next && next !== prev) writeStoredModel(next);
      return next || prev;
    });
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [modelsResult, settingsResult] = await Promise.allSettled([fetchModels(), fetchSettings()]);
      if (cancelled) return;
      const list = modelsResult.status === 'fulfilled' && Array.isArray(modelsResult.value) ? modelsResult.value : [];
      const settings = settingsResult.status === 'fulfilled' ? settingsResult.value : null;
      if (list.length) setModels(list);
      if (settings) {
        if (typeof settings.auto_fallback === 'boolean') setAutoFallback(settings.auto_fallback);
        if (settings.theme) setTheme(saveThemePreference(settings.theme));
      }

      const preferredModel = (settings && settings.default_model) || readStoredModel() || 'minimax-m3';
      const nextModel = pickValidModel(list, preferredModel);
      if (nextModel) {
        setModel(nextModel);
        writeStoredModel(nextModel);
        if (settings && (settings.default_model || '') !== nextModel) {
          updateSettings({ default_model: nextModel }).catch(() => {});
        }
      }

      const preferredFallback = settings && typeof settings.fallback_model === 'string'
        ? settings.fallback_model
        : readFallbackModel();
      const nextFallback = pickValidFallbackModel(list, preferredFallback, nextModel);
      setFallbackModel(nextFallback);
      writeFallbackModel(nextFallback);
      if (settings && (settings.fallback_model || '') !== nextFallback) {
        updateSettings({ fallback_model: nextFallback }).catch(() => {});
      }
    })().catch(() => {});
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (!models.length) return;
    const next = pickValidFallbackModel(models, fallbackModel, model);
    if (next !== fallbackModel) {
      setFallbackModel(next);
      writeFallbackModel(next);
      updateSettings({ fallback_model: next }).catch(() => {});
    }
  }, [models, model, fallbackModel]);

  const handleAutoFallbackChange = useCallback((nextValue) => {
    setAutoFallback(prev => {
      const next = typeof nextValue === 'boolean' ? nextValue : !prev;
      writeAutoFallback(next);
      updateSettings({ auto_fallback: next }).catch(() => {});
      return next;
    });
  }, []);

  const handleFallbackModelChange = useCallback((modelId) => {
    const next = String(modelId || '');
    setFallbackModel(next);
    writeFallbackModel(next);
    updateSettings({ fallback_model: next }).catch(() => {});
  }, []);

  const handleVoiceAutoReadChange = useCallback((enabled) => {
    const next = enabled !== false;
    setVoiceAutoRead(next);
    writeVoiceAutoRead(next, 'voice');
  }, []);

  const handleThemeChange = useCallback((nextTheme) => {
    const resolved = saveThemePreference(nextTheme);
    setTheme(resolved);
    updateSettings({ theme: resolved }).catch(() => {});
  }, []);

  useEffect(() => {
    const onStorage = (event) => {
      if (event.key === MODEL_STORAGE_KEY && event.newValue) setModel(event.newValue);
      if (event.key === FALLBACK_ENABLED_STORAGE_KEY) setAutoFallback(event.newValue !== '0');
      if (event.key === FALLBACK_MODEL_STORAGE_KEY) setFallbackModel(event.newValue || '');
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, []);

  return (
    <div className="voice-page">
      <button
        className="voice-page-back"
        onClick={() => {
          // 只清 hash 只能从 #/voice 退回主界面；在 /agent/voice 这种独立路径下
          // pathname 仍然是 voice 页，清 hash 没有任何作用。统一跳到应用根路径，
          // 并保留线上 Caddy 挂载的 /agent 前缀。
          window.location.replace(appBase() + '/');
        }}
        title="返回聊天主界面"
        aria-label="返回聊天主界面"
      />
      <VoiceDock
        models={models}
        onModelsChanged={handleModelsChanged}
        model={model}
        onModelChange={handleModelChange}
        sessionName={sessionName}
        channel={voiceChannel}
        muted={!voiceAutoRead}
        onMutedChange={nextMuted => handleVoiceAutoReadChange(!nextMuted)}
        autoFallback={autoFallback}
        onAutoFallbackChange={handleAutoFallbackChange}
        fallbackModel={fallbackModel}
        onFallbackModelChange={handleFallbackModelChange}
        onOpenSettings={() => setSettingsOpen(true)}
        onTurnComplete={() => {}}
        onSelectSession={name => setSessionName(name || (voiceChannel ? voiceSessionName(voiceChannel) : ''))}
      />
      <SettingsMenu
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        models={models}
        selectedModel={model}
        onSelectModel={handleModelChange}
        theme={theme}
        onThemeChange={handleThemeChange}
        voiceAutoRead={voiceAutoRead}
        onVoiceAutoReadChange={handleVoiceAutoReadChange}
        autoFallback={autoFallback}
        onAutoFallbackChange={handleAutoFallbackChange}
        fallbackModel={fallbackModel}
        onFallbackModelChange={handleFallbackModelChange}
        onModelsChanged={handleModelsChanged}
      />
    </div>
  );
}
