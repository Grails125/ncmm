(() => {
  'use strict';

  const nativeVersion = /^v?\d+\.\d+\.\d+-grails\.\d+$/;

  function render(policy) {
    // 是否到期由服务端决定；浏览器不把缓存、网络错误或未知分发版本升级为阻断。
    const visible = !!policy && nativeVersion.test(policy.currentVersion || '') && nativeVersion.test(policy.requiredVersion || '');
    for (const banner of document.querySelectorAll('[data-upgrade-policy]')) {
      banner.hidden = !visible;
      if (!visible) continue;
      const required = policy.required === true;
      banner.classList.toggle('upgrade-policy-required', required);
      banner.querySelector('[data-upgrade-title]').textContent = required ? '需要升级后继续任务' : '版本升级提醒';
      const deadline = new Date(policy.graceUntil);
      const grace = Number.isNaN(deadline.getTime()) ? '' : `请于 ${deadline.toLocaleString('zh-CN', { hour12: false })} 前升级，`;
      banner.querySelector('[data-upgrade-message]').textContent = `当前版本 ${policy.currentVersion}，要求版本 ${policy.requiredVersion}。${required ? '宽限期已结束，新的手动及定时任务已暂停。登录、停止任务和系统更新仍可用。' : `${grace}当前仍可执行任务。`}`;
      // 发布链接由已校验的版本构造，不信任缓存中的任意 URL 或 HTML。
      banner.querySelector('[data-upgrade-release]').href = `https://github.com/Grails125/ncmm/releases/tag/${encodeURIComponent(policy.requiredVersion)}`;
    }
  }

  globalThis.NCMMUpgradePolicy = Object.freeze({ render });
})();
