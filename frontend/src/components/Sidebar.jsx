import React from 'react';
import { sessionDateLabel, sessionRouteLabel } from '../utils/sessionDate';

function buildSessionTree(sessions) {
  const list = Array.isArray(sessions) ? sessions : [];
  const byParent = new Map();
  const roots = [];
  for (const session of list) {
    const parent = String(session && session.parent_session || '').trim();
    if (parent) {
      if (!byParent.has(parent)) byParent.set(parent, []);
      byParent.get(parent).push(session);
    } else {
      roots.push(session);
    }
  }
  return { roots, byParent };
}

function collapseBranchRetries(branches, selectedName) {
  const groups = new Map();
  for (const branch of branches) {
    if (!branch) continue;
    const key = String(branch.domain || branch.name || '').trim();
    if (!key) continue;
    const existing = groups.get(key);
    if (!existing) {
      groups.set(key, { session: branch, attempts: 1 });
      continue;
    }
    existing.attempts += 1;
    const currentSelected = existing.session.name === selectedName;
    const nextSelected = branch.name === selectedName;
    const currentRunning = !!existing.session._running;
    const nextRunning = !!branch._running;
    if (nextSelected || (!currentSelected && (nextRunning || (!currentRunning && String(branch.modified || '') > String(existing.session.modified || ''))))) {
      existing.session = branch;
    }
  }
  return Array.from(groups.values());
}

export default function Sidebar({ sessions, sessionName, onSelect, onNew, onNewBranch, onDeleteSession, autoCollapse, runningSubtasks = {} }) {
  const [isCollapsed, setIsCollapsed] = React.useState(() => (
    typeof window !== 'undefined' && window.matchMedia('(max-width: 760px)').matches
  ));
  const [collapsedParents, setCollapsedParents] = React.useState({});
  const { roots, byParent } = React.useMemo(() => buildSessionTree(sessions), [sessions]);
  const selected = (sessions || []).find(session => session.name === sessionName);

  React.useEffect(() => {
    const media = window.matchMedia('(max-width: 760px)');
    const sync = () => setIsCollapsed(media.matches);
    sync();
    media.addEventListener('change', sync);
    return () => media.removeEventListener('change', sync);
  }, []);

  React.useEffect(() => {
    if (autoCollapse) setIsCollapsed(true);
  }, [autoCollapse]);

  React.useEffect(() => {
    if (selected && selected.parent_session) {
      setCollapsedParents(prev => ({ ...prev, [selected.parent_session]: false }));
    }
  }, [selected && selected.name, selected && selected.parent_session]);

  const toggleCollapse = () => setIsCollapsed(!isCollapsed);
  const toggleParent = (parentName, event) => {
    event.stopPropagation();
    setCollapsedParents(prev => ({ ...prev, [parentName]: !prev[parentName] }));
  };

  return (
    <div className={'sidebar-wrap' + (isCollapsed ? ' collapsed' : '')}>
      <aside className="sidebar">
        <div className="sidebar-brand">
          <img className="sidebar-brand-logo" src="/fairy.png" alt="Fairy" draggable={false} />
          <div className="sidebar-brand-text">
            <strong>FAIRY</strong>
            <span>Agent Workbench</span>
          </div>
        </div>
        <h2>每日会话</h2>
        <button className="new-btn" onClick={onNew}>进入今日会话</button>
        <button
          className="new-btn new-btn-branch"
          onClick={onNewBranch || onNew}
          title="在当前主会话下创建并立即进入一个空子分支"
        >新对话</button>
        <div className="session-list">
          {roots.length === 0 && <div className="empty-list">暂无历史对话</div>}
          {roots.map(parent => {
            const branches = (byParent.get(parent.name) || []).map(branch => ({
              ...branch,
              _running: !!runningSubtasks[branch.name],
            }));
            const branchGroups = collapseBranchRetries(branches, sessionName);
            const knownBranchNames = new Set(branchGroups.map(group => group.session.name));
            const pendingRunningBranches = Object.values(runningSubtasks)
              .filter(item => item && item.parent === parent.name && !knownBranchNames.has(item.name))
              .map(item => ({
                attempts: 1,
                session: {
                  name: item.name,
                  parent_session: parent.name,
                  domain: item.domain || item.name,
                  preview: '',
                  message_count: 0,
                  _running: true,
                },
              }));
            const displayBranchGroups = [...branchGroups, ...pendingRunningBranches];
            const hasBranches = displayBranchGroups.length > 0;
            const collapsed = !!collapsedParents[parent.name];
            const routeLabel = sessionRouteLabel(parent);
            // 渠道会话是长期存在的一条对话，用"途径"当标题、用日期当副标签
            // （反过来会显示成一个日期，看起来像当天主会话的一份副本）。
            const parentTitle = routeLabel || parent.daily_date || sessionDateLabel(parent);
            const parentSubLabel = routeLabel ? sessionDateLabel(parent) : '';
            const parentTitleHint = parent.channel && parent.channel.conversation_id
              ? String(parent.channel.conversation_id)
              : undefined;
            return (
              <div className="session-group" key={parent.name}>
                <div
                  className={'session' + (parent.name === sessionName ? ' active' : '')}
                  onClick={() => onSelect(parent.name)}
                >
                  <div className="session-date">
                    {hasBranches ? (
                      <button
                        type="button"
                        className={'session-branch-toggle' + (collapsed ? ' collapsed' : '')}
                        onClick={event => toggleParent(parent.name, event)}
                        aria-label={collapsed ? '展开分支会话' : '收起分支会话'}
                      >
                        {collapsed ? '▸' : '▾'}
                      </button>
                    ) : null}
                    <span title={parentTitleHint}>{parentTitle}</span>
                    {parentSubLabel ? <span className="session-route">{parentSubLabel}</span> : null}
                    {hasBranches ? <span className="session-branch-count">{displayBranchGroups.length}</span> : null}
                  </div>
                  <div className={'preview' + (parent.preview ? '' : ' empty-preview')}>
                    {parent.preview || '(新对话，空的)'}
                  </div>
                  <div className="meta">
                    {parent.model && <span className="model-badge">{parent.model}</span>}
                    <span>{parent.message_count}条 · {parent.modified ? parent.modified.slice(11, 19) : ''}</span>
                  </div>
                </div>
                {hasBranches && !collapsed ? (
                  <div className="session-branches">
                    {displayBranchGroups.map(({ session: branch, attempts }) => (
                      <div
                        key={branch.name}
                        className={'session session-branch' + (branch.name === sessionName ? ' active' : '') + (branch._running ? ' running' : '')}
                        onClick={() => onSelect(branch.name)}
                        title={attempts > 1 ? `${attempts} 次尝试，显示最新一次` : branch.name}
                      >
                        <div className="session-date">
                          <span className={'branch-mark' + (branch._running ? ' running' : '')}>{branch._running ? '◌' : '└'}</span>
                          <span>{branch.domain || branch.name}</span>
                          {branch._running ? <span className="branch-running">执行中</span> : null}
                          {onDeleteSession ? (
                            <button
                              type="button"
                              className="session-delete-btn"
                              onClick={(event) => { event.stopPropagation(); onDeleteSession(branch.name); }}
                              title="删除该分支会话"
                              aria-label="删除该分支会话"
                            >×</button>
                          ) : null}
                        </div>
                        <div className={'preview' + (branch.preview ? '' : ' empty-preview')}>
                          {branch.preview || (branch._running ? '正在执行…' : '(分支会话，空的)')}
                        </div>
                        <div className="meta">
                          {branch.model && <span className="model-badge">{branch.model}</span>}
                          {attempts > 1 ? <span className="branch-retry-badge">尝试{attempts}次</span> : null}
                          <span>{branch.message_count}条 · {branch.modified ? branch.modified.slice(11, 19) : ''}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </aside>
      <button
        className="sidebar-collapse-tab"
        onClick={toggleCollapse}
        title={isCollapsed ? '展开侧边栏' : '收起侧边栏'}
        aria-label={isCollapsed ? '展开侧边栏' : '收起侧边栏'}
      >
        {isCollapsed ? '▸' : '◂'}
      </button>
    </div>
  );
}
