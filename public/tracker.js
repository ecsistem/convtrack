(function (window, document) {
  'use strict';

  var CT_VERSION      = '2.2.0';
  var STORAGE_VISITOR = 'ct_vid';
  var STORAGE_SESSION = 'ct_sid';
  var STORAGE_ATTR    = 'ct_attr';
  var SESSION_TIMEOUT = 30 * 60 * 1000; // 30 min inactivity = new session

  var script = document.currentScript || (function () {
    var scripts = document.getElementsByTagName('script');
    return scripts[scripts.length - 1];
  })();

  var apiKey        = script.getAttribute('data-key')    || '';
  var allowedDomain = script.getAttribute('data-domain') || '';
  var apiBase       = script.getAttribute('data-api')    || (function () {
    try { return new URL(script.src).origin; } catch (e) { return ''; }
  })();
  var replayOn = script.getAttribute('data-replay') !== 'false';

  if (!apiKey) {
    console.warn('[ConvTrack] Missing data-key attribute.');
    return;
  }

  // ── Anti-Clone Protection (multicamadas, imediata) ─────────────────────────
  // Roda ANTES de qualquer chamada à API. Não depende de resposta do servidor.

  (function initCloneProtection() {
    if (!allowedDomain) return; // sem data-domain → proteção API-only

    function normHost(h) {
      return h.toLowerCase()
        .replace(/^https?:\/\//, '')
        .replace(/^www\./, '')
        .split('/')[0]
        .split(':')[0];
    }

    var allowed = normHost(allowedDomain);
    var current = normHost(window.location.hostname);

    function targetURL() {
      var base = /^https?:\/\//.test(allowedDomain) ? allowedDomain : 'https://' + allowedDomain;
      return base.replace(/\/$/, '') + window.location.pathname + window.location.search + window.location.hash;
    }

    // Camada 1 — domínio diferente
    var domainOK = (current === allowed) || current.endsWith('.' + allowed);
    if (!domainOK) {
      try { document.body.style.display = 'none'; } catch (e) {}
      window.location.replace(targetURL());
      return;
    }

    // Camada 2 — iframe cross-origin
    if (window.self !== window.top) {
      try {
        window.top.location.replace(window.location.href);
      } catch (e) {
        window.location.replace(targetURL());
      }
      return;
    }

    // Camada 3 — recheck periódico (resistência a manipulação de script)
    var _checkInterval = setInterval(function () {
      var cur = normHost(window.location.hostname);
      if (cur !== allowed && !cur.endsWith('.' + allowed)) {
        clearInterval(_checkInterval);
        window.location.replace(targetURL());
      }
    }, 3000);

  })();

  // ── Utilities ─────────────────────────────────────────────────────────────

  function uuid() {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID();
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
      var r = (Math.random() * 16) | 0;
      return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
    });
  }

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) {}
    try {
      var exp = new Date(Date.now() + 365 * 24 * 3600 * 1000).toUTCString();
      document.cookie = key + '=' + encodeURIComponent(value) + '; expires=' + exp + '; path=/; SameSite=Lax';
    } catch (e) {}
  }

  function load(key) {
    try { var v = localStorage.getItem(key); if (v) return v; } catch (e) {}
    try {
      var m = document.cookie.match(new RegExp('(?:^|;)\\s*' + key + '=([^;]+)'));
      return m ? decodeURIComponent(m[1]) : null;
    } catch (e) { return null; }
  }

  function getCookie(name) {
    var m = document.cookie.match(new RegExp('(?:^|;)\\s*' + name + '=([^;]+)'));
    return m ? decodeURIComponent(m[1]) : '';
  }

  function getParam(name) {
    try { return new URLSearchParams(window.location.search).get(name) || ''; }
    catch (e) {
      var m = window.location.search.match(new RegExp('[?&]' + name + '=([^&]+)'));
      return m ? decodeURIComponent(m[1]) : '';
    }
  }

  // fire-and-forget: usa sendBeacon quando disponível (sobrevive ao beforeunload),
  // com fallback para XHR assíncrono.
  function send(path, body) {
    var data = JSON.stringify(Object.assign({ api_key: apiKey }, body));
    if (navigator.sendBeacon) {
      navigator.sendBeacon(apiBase + path, new Blob([data], { type: 'application/json' }));
    } else {
      var xhr = new XMLHttpRequest();
      xhr.open('POST', apiBase + path, true);
      xhr.setRequestHeader('Content-Type', 'application/json');
      xhr.setRequestHeader('X-API-Key', apiKey);
      xhr.send(data);
    }
  }

  // ── Visitor & Session ─────────────────────────────────────────────────────

  var visitorId = load(STORAGE_VISITOR);
  if (!visitorId) { visitorId = uuid(); store(STORAGE_VISITOR, visitorId); }

  function needsNewSession() {
    var ts = load(STORAGE_SESSION + '_ts');
    return !ts || (Date.now() - parseInt(ts, 10) > SESSION_TIMEOUT);
  }

  var sessionId = load(STORAGE_SESSION);
  if (!sessionId || needsNewSession()) { sessionId = uuid(); store(STORAGE_SESSION, sessionId); }
  store(STORAGE_SESSION + '_ts', String(Date.now()));

  // ── UTM & Click ID capture ────────────────────────────────────────────────

  var utmKeys   = ['utm_source', 'utm_medium', 'utm_campaign', 'utm_content', 'utm_term'];
  var clickKeys = ['fbclid', 'gclid', 'ttclid', 'kwclid'];
  var attr = {};
  var hasNewAttribution = false;

  utmKeys.concat(clickKeys).forEach(function (k) {
    var v = getParam(k);
    if (v) { attr[k] = v; hasNewAttribution = true; }
  });

  if (hasNewAttribution) {
    store(STORAGE_ATTR, JSON.stringify(attr));
  } else {
    try { attr = JSON.parse(load(STORAGE_ATTR) || '{}'); } catch (e) {}
  }

  // FBP & FBC
  attr.fbp = getCookie('_fbp');
  attr.fbc  = getCookie('_fbc');
  if (!attr.fbc && attr.fbclid) {
    attr.fbc = 'fb.1.' + Date.now() + '.' + attr.fbclid;
  }

  // ── Device & browser metadata ─────────────────────────────────────────────

  var screenW  = (window.screen && window.screen.width)  || 0;
  var screenH  = (window.screen && window.screen.height) || 0;
  var timezone = '';
  try { timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (e) {}
  // FIX #8: navigator.language tem prioridade; userLanguage era IE-only
  var language = navigator.language || '';

  // ── Engagement / duration tracking ───────────────────────────────────────

  var sessionStartTs = Date.now();
  var engagedMs      = 0;
  var engagedStart   = null;
  var pageCount      = 1;
  var currentPage    = window.location.href;
  var heartbeatTimer = null;
  var HEARTBEAT_MS   = 30 * 1000;

  var clickCount     = 0;
  var inputCount     = 0;
  var scrollDepthPct = 0;
  var rageClicks     = 0;
  var _rageTs        = [];
  var _rageTarget    = null;

  document.addEventListener('click', function (e) {
    clickCount++;
    var now = Date.now();
    if (e.target === _rageTarget) {
      _rageTs.push(now);
      _rageTs = _rageTs.filter(function (t) { return now - t < 500; });
      if (_rageTs.length >= 3) { rageClicks++; _rageTs = []; }
    } else { _rageTarget = e.target; _rageTs = [now]; }
  }, true);

  document.addEventListener('input', function (e) {
    var tag = (e.target && e.target.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea' || e.target.isContentEditable) inputCount++;
  }, true);

  function updateScrollDepth() {
    var el      = document.documentElement;
    var scrolled = (window.pageYOffset || el.scrollTop) + window.innerHeight;
    var total    = el.scrollHeight || 1;
    var pct      = Math.round((scrolled / total) * 100);
    if (pct > scrollDepthPct) scrollDepthPct = Math.min(pct, 100);
  }
  window.addEventListener('scroll', updateScrollDepth, { passive: true });

  function isActive() { return document.visibilityState !== 'hidden'; }

  function startEngaged() { if (engagedStart === null && isActive()) engagedStart = Date.now(); }
  function pauseEngaged()  {
    if (engagedStart !== null) { engagedMs += Date.now() - engagedStart; engagedStart = null; }
  }

  startEngaged();
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') pauseEngaged(); else startEngaged();
  });
  window.addEventListener('focus', startEngaged);
  window.addEventListener('blur',  pauseEngaged);

  function totalDurationSeconds() { return Math.round((Date.now() - sessionStartTs) / 1000); }

  function sendHeartbeat(isFinal) {
    pauseEngaged();
    updateScrollDepth();
    var body = {
      session_id:       sessionId,
      duration_seconds: totalDurationSeconds(),
      page_count:       pageCount,
      current_page:     currentPage,
      click_count:      clickCount,
      input_count:      inputCount,
      scroll_depth_pct: scrollDepthPct,
      rage_clicks:      rageClicks,
    };
    if (isFinal && navigator.sendBeacon) {
      navigator.sendBeacon(
        apiBase + '/v1/collect/heartbeat',
        new Blob([JSON.stringify(Object.assign({ api_key: apiKey }, body))], { type: 'application/json' })
      );
    } else {
      send('/v1/collect/heartbeat', body);
    }
    if (!isFinal) startEngaged();
  }

  function scheduleHeartbeat() {
    clearTimeout(heartbeatTimer);
    heartbeatTimer = setTimeout(function () { sendHeartbeat(false); scheduleHeartbeat(); }, HEARTBEAT_MS);
  }

  scheduleHeartbeat();
  window.addEventListener('beforeunload', function () { sendHeartbeat(true); });

  // ── Send session to API ───────────────────────────────────────────────────

  function collectSession() {
    fetch(apiBase + '/v1/collect/session', {
      method:    'POST',
      headers:   { 'Content-Type': 'application/json', 'X-API-Key': apiKey },
      body:      JSON.stringify(Object.assign({ api_key: apiKey }, {
        visitor_id:    visitorId,
        session_id:    sessionId,
        landing_page:  window.location.href,
        referrer:      document.referrer,
        user_agent:    navigator.userAgent,
        screen_width:  screenW,
        screen_height: screenH,
        timezone:      timezone,
        language:      language,
        sdk_version:   CT_VERSION,
      }, attr)),
      keepalive: true,
    })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        // Camada 4 — clone detection via resposta da API (DomainCheck middleware)
        if (data && data.clone_detected && data.redirect_url) {
          try { document.body.style.display = 'none'; } catch (e) {}
          window.location.replace(data.redirect_url);
        }
      })
      .catch(function () {});
  }

  collectSession();

  // ── Public API ────────────────────────────────────────────────────────────

  window.ConvTrack = {
    version: CT_VERSION,

    track: function (eventName, properties) {
      if (!eventName) { console.warn('[ConvTrack] track() requires eventName'); return; }
      send('/v1/collect/event', {
        session_id: sessionId,
        name:       eventName,
        properties: properties || {},
      });
    },

    identify: function (email, phone) {
      if (!email && !phone) return;
      send('/v1/collect/identify', {
        visitor_id: visitorId,
        session_id: sessionId,
        email:      email || '',
        phone:      phone || '',
      });
    },

    // Disparo server-side direto (bypassa regras, aciona CAPI/TikTok/etc.)
    conversion: function (eventName, opts) {
      if (!eventName) { console.warn('[ConvTrack] conversion() requires eventName'); return; }
      opts = opts || {};
      send('/v1/collect/conversion', {
        session_id: sessionId,
        rule_id:    opts.rule_id  || '',
        event_name: eventName,
        value:      opts.value    || 0,
        currency:   opts.currency || 'BRL',
      });
    },

    getSessionId:    function () { return sessionId; },
    getVisitorId:    function () { return visitorId; },
    getAttribution:  function () { return Object.assign({}, attr); },
    getAllowedDomain: function () { return allowedDomain; },
  };

  // ── Unified SPA navigation dispatcher ────────────────────────────────────
  // FIX #2: Uma única camada de patches em history.pushState/replaceState.
  // Outros módulos (replay, rules) registram callbacks aqui, em vez de
  // fazerem seus próprios patches — evita cadeia frágil de sobreposições.

  var _navListeners = [];
  var _origPush     = history.pushState;
  var _origReplace  = history.replaceState;

  function _onNav() {
    if (window.location.pathname !== currentPage.replace(/[?#].*$/, '').replace(window.location.origin, '')) {
      // Detecta mudança real de pathname
    }
    // Notifica todos os listeners registrados
    _navListeners.forEach(function (fn) {
      try { fn(window.location.href); } catch (e) {}
    });
  }

  history.pushState = function () {
    _origPush.apply(history, arguments);
    setTimeout(_onNav, 0); // aguarda DOM atualizar
  };
  history.replaceState = function () {
    _origReplace.apply(history, arguments);
    setTimeout(_onNav, 0);
  };
  window.addEventListener('popstate', function () { setTimeout(_onNav, 0); });

  // ── Visual Inspector ─────────────────────────────────────────────────────
  // Ativado via ?tracker_inspect=1 na URL.
  // FIX #4: aguarda DOMContentLoaded para evitar crash quando script está no <head>.

  (function initInspector() {
    if (!getParam('tracker_inspect')) return;

    function setup() {
      // Estilos
      var style = document.createElement('style');
      style.textContent = [
        '#ct-inspector-banner{all:initial;position:fixed;top:0;left:0;right:0;z-index:2147483647;',
        'background:#4f46e5;color:#fff;font:600 13px/1 -apple-system,sans-serif;',
        'padding:10px 16px;display:flex;align-items:center;gap:12px;',
        'box-shadow:0 2px 12px rgba(0,0,0,.35);}',
        '#ct-inspector-banner span{flex:1}',
        '#ct-inspector-banner kbd{background:rgba(255,255,255,.15);border-radius:4px;padding:2px 6px;font:inherit;font-size:11px;}',
        '#ct-inspector-banner button{background:rgba(255,255,255,.15);border:none;color:#fff;cursor:pointer;border-radius:6px;padding:4px 10px;font:inherit;}',
        '#ct-inspector-banner button:hover{background:rgba(255,255,255,.3)}',
        '#ct-inspector-highlight{position:fixed;pointer-events:none;z-index:2147483646;',
        'border:2px solid #4f46e5;border-radius:3px;background:rgba(79,70,229,.08);',
        'transition:all .08s ease;box-shadow:0 0 0 9999px rgba(0,0,0,.18);}',
        '#ct-inspector-tooltip{position:fixed;z-index:2147483647;pointer-events:none;',
        'background:#1e1b4b;color:#c7d2fe;font:500 11px/1.4 monospace;',
        'padding:4px 8px;border-radius:5px;white-space:nowrap;max-width:340px;',
        'overflow:hidden;text-overflow:ellipsis;box-shadow:0 2px 8px rgba(0,0,0,.4);}',
        '#ct-inspector-panel{position:fixed;bottom:0;left:0;right:0;z-index:2147483647;',
        'background:#1e1b4b;color:#e0e7ff;font:13px/1.6 -apple-system,sans-serif;',
        'padding:16px 20px 20px;box-shadow:0 -4px 20px rgba(0,0,0,.4);',
        'border-top:1px solid #3730a3;transform:translateY(100%);transition:transform .2s ease;}',
        '#ct-inspector-panel.ct-open{transform:translateY(0)}',
        '#ct-inspector-panel h3{margin:0 0 10px;font-size:12px;text-transform:uppercase;letter-spacing:.08em;color:#818cf8;}',
        '.ct-panel-row{display:flex;gap:8px;margin-bottom:8px;align-items:flex-start}',
        '.ct-panel-row label{font-size:11px;color:#818cf8;min-width:80px;padding-top:4px;flex-shrink:0}',
        '.ct-code{flex:1;background:#312e81;border-radius:6px;padding:7px 10px;font:12px/1.5 monospace;',
        'color:#c7d2fe;word-break:break-all;border:1px solid #3730a3;}',
        '.ct-copy-btn{flex-shrink:0;background:#4f46e5;border:none;color:#fff;cursor:pointer;',
        'border-radius:6px;padding:6px 10px;font-size:12px;white-space:nowrap;}',
        '.ct-copy-btn:hover{background:#4338ca}.ct-copy-btn.ct-copied{background:#059669}',
        '.ct-close-panel{float:right;background:none;border:none;color:#818cf8;cursor:pointer;font-size:18px;line-height:1;padding:0 0 4px 8px;}',
      ].join('');
      document.head.appendChild(style);

      var banner = document.createElement('div');
      banner.id = 'ct-inspector-banner';
      banner.innerHTML = '<span>🔍 <strong>ConvTrack Inspector</strong> — Passe o mouse e clique no elemento que quer rastrear</span><kbd>Esc</kbd> para sair<button id="ct-exit-btn">✕ Sair</button>';
      document.body.appendChild(banner);
      document.body.style.marginTop = (parseInt(document.body.style.marginTop || 0) + (banner.offsetHeight || 44)) + 'px';

      var highlight = document.createElement('div');
      highlight.id = 'ct-inspector-highlight';
      highlight.style.display = 'none';
      document.body.appendChild(highlight);

      var tooltip = document.createElement('div');
      tooltip.id = 'ct-inspector-tooltip';
      tooltip.style.display = 'none';
      document.body.appendChild(tooltip);

      var panel = document.createElement('div');
      panel.id = 'ct-inspector-panel';
      panel.innerHTML = '<button class="ct-close-panel" id="ct-close-panel">✕</button><h3>Elemento selecionado</h3><div id="ct-panel-body"></div>';
      document.body.appendChild(panel);

      function cssEscape(s) {
        return typeof CSS !== 'undefined' && CSS.escape ? CSS.escape(s) : s.replace(/([^\w-])/g, '\\$1');
      }

      function getSelector(el) {
        if (el.id && document.querySelectorAll('#' + cssEscape(el.id)).length === 1) return '#' + cssEscape(el.id);
        var path = [];
        var cur  = el;
        while (cur && cur !== document.body && cur !== document.documentElement) {
          var tag  = cur.tagName.toLowerCase();
          var part = tag;
          if (cur.id) { path.unshift('#' + cssEscape(cur.id)); break; }
          var goodClasses = [];
          for (var i = 0; i < cur.classList.length; i++) {
            var c = cur.classList[i];
            if (!/^[a-z0-9]{8,}$/.test(c) && !/^[A-Z]/.test(c)) { goodClasses.push('.' + cssEscape(c)); if (goodClasses.length >= 2) break; }
          }
          if (goodClasses.length) part = tag + goodClasses.join('');
          var parent = cur.parentElement;
          if (parent) {
            var same = Array.prototype.filter.call(parent.children, function (s) { return s.tagName === cur.tagName; });
            if (same.length > 1) part += ':nth-child(' + (Array.prototype.indexOf.call(parent.children, cur) + 1) + ')';
          }
          path.unshift(part);
          try { if (document.querySelectorAll(path.join(' > ')).length === 1) break; } catch (e) { break; }
          cur = cur.parentElement;
        }
        return path.join(' > ');
      }

      function getTagInfo(el) {
        var info = el.tagName.toLowerCase();
        if (el.id)               info += '#' + el.id;
        if (el.classList.length) info += '.' + Array.prototype.join.call(el.classList, '.');
        if (el.name)             info += '[name="' + el.name + '"]';
        return info;
      }

      function escHtml(s) { return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

      function copyText(text, btn) {
        var prev = btn.textContent;
        btn.classList.add('ct-copied'); btn.textContent = '✓ Copiado!';
        setTimeout(function () { btn.classList.remove('ct-copied'); btn.textContent = prev; }, 2000);
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text);
        } else {
          var ta = document.createElement('textarea');
          ta.value = text; ta.style.cssText = 'position:fixed;left:-9999px';
          document.body.appendChild(ta); ta.select(); document.execCommand('copy'); document.body.removeChild(ta);
        }
      }

      function showPanel(selector, snippet) {
        var body = document.getElementById('ct-panel-body');
        body.innerHTML = [
          '<div class="ct-panel-row"><label>Seletor</label>',
          '<code class="ct-code" id="ct-sel-code">', escHtml(selector), '</code>',
          '<button class="ct-copy-btn" id="ct-copy-sel">Copiar</button></div>',
          '<div class="ct-panel-row"><label>Snippet</label>',
          '<code class="ct-code" style="white-space:pre">', escHtml(snippet), '</code>',
          '<button class="ct-copy-btn" id="ct-copy-snip">Copiar</button></div>',
        ].join('');
        panel.classList.add('ct-open');
        document.getElementById('ct-copy-sel').onclick  = function () { copyText(selector, this); };
        document.getElementById('ct-copy-snip').onclick = function () { copyText(snippet,  this); };
        copyText(selector, document.getElementById('ct-copy-sel'));
      }

      var lastTarget = null;

      function onMouseMove(e) {
        var el = e.target;
        if (banner.contains(el) || panel.contains(el) || el === highlight || el === tooltip) return;
        if (el === lastTarget) return;
        lastTarget = el;
        var r = el.getBoundingClientRect();
        highlight.style.display = 'block';
        highlight.style.top     = r.top    + 'px';
        highlight.style.left    = r.left   + 'px';
        highlight.style.width   = r.width  + 'px';
        highlight.style.height  = r.height + 'px';
        tooltip.style.display = 'block';
        tooltip.textContent   = getTagInfo(el);
        var tx = r.left;
        var ty = r.bottom + 6;
        if (ty + 30 > window.innerHeight) ty = r.top - 30;
        tooltip.style.top  = ty + 'px';
        tooltip.style.left = tx + 'px';
      }

      function onClickCapture(e) {
        var el = e.target;
        if (el === document.getElementById('ct-exit-btn') || el === document.getElementById('ct-close-panel')) return;
        if (banner.contains(el) || panel.contains(el)) return;
        e.preventDefault(); e.stopPropagation();
        var selector = getSelector(el);
        var snippet  = 'document.querySelector(\'' + selector + '\')\n  .addEventListener(\'click\', function () {\n    ConvTrack.track(\'meu_evento\', { page: location.href });\n  });';
        showPanel(selector, snippet);
      }

      function exitInspector() {
        document.removeEventListener('mousemove', onMouseMove);
        document.removeEventListener('click', onClickCapture, true);
        document.removeEventListener('keydown', onKeyDown);
        [banner, highlight, tooltip, panel].forEach(function (el) { if (el.parentNode) el.parentNode.removeChild(el); });
        document.body.style.marginTop = '';
        try {
          var url = new URL(window.location.href);
          url.searchParams.delete('tracker_inspect');
          history.replaceState(null, '', url.toString());
        } catch (e) {}
      }

      function onKeyDown(e) { if (e.key === 'Escape') exitInspector(); }

      document.getElementById('ct-exit-btn').addEventListener('click', exitInspector);
      document.getElementById('ct-close-panel').addEventListener('click', function () { panel.classList.remove('ct-open'); });
      document.addEventListener('mousemove', onMouseMove);
      document.addEventListener('click', onClickCapture, true);
      document.addEventListener('keydown', onKeyDown);
    }

    // FIX #4: guarda DOM antes de manipular document.body
    if (document.body) {
      setup();
    } else {
      document.addEventListener('DOMContentLoaded', setup);
    }

  })();

  // ── SPA pageview tracking ─────────────────────────────────────────────────
  // FIX #2: registra callback no dispatcher unificado em vez de novo patch.

  var _lastPath = window.location.pathname;

  _navListeners.push(function (href) {
    if (window.location.pathname !== _lastPath) {
      _lastPath   = window.location.pathname;
      currentPage = href;
      pageCount  += 1;
      store(STORAGE_SESSION + '_ts', String(Date.now()));
      window.ConvTrack.track('pageview', { url: href });
      sendHeartbeat(false);
      scheduleHeartbeat();
    }
  });

  // ── Session Replay (rrweb) ────────────────────────────────────────────────

  if (replayOn) {
    var replayTriggers = (script.getAttribute('data-replay-triggers') || 'checkout,order,obrigado,thank,lead').split(',');
    var currentPath = window.location.pathname.toLowerCase();
    var shouldRecord = replayTriggers.some(function (t) { return currentPath.indexOf(t.trim()) !== -1; });
    var replayTrigger = '';
    replayTriggers.forEach(function (t) { if (currentPath.indexOf(t.trim()) !== -1) replayTrigger = t.trim(); });

    if (shouldRecord && typeof rrweb !== 'undefined') {
      var replayBatch = [];
      var flushTimer;
      var BATCH_SIZE  = 50;
      var FLUSH_MS    = 5000;

      function sendBatch(final) {
        if (replayBatch.length === 0 && !final) return;
        var toSend = replayBatch.splice(0);
        if (final) {
          if (toSend.length > 0) send('/v1/replay/events', { session_id: sessionId, trigger: replayTrigger, events: toSend });
          var flushBody = JSON.stringify({ api_key: apiKey, session_id: sessionId, trigger: replayTrigger });
          if (navigator.sendBeacon) {
            navigator.sendBeacon(apiBase + '/v1/replay/flush', new Blob([flushBody], { type: 'application/json' }));
          } else {
            send('/v1/replay/flush', { session_id: sessionId, trigger: replayTrigger });
          }
        } else {
          send('/v1/replay/events', { session_id: sessionId, trigger: replayTrigger, events: toSend });
        }
      }

      function scheduledFlush() {
        sendBatch(false);
        clearTimeout(flushTimer);
        flushTimer = setTimeout(scheduledFlush, FLUSH_MS);
      }

      rrweb.record({
        emit: function (event) {
          replayBatch.push(event);
          if (replayBatch.length >= BATCH_SIZE) { sendBatch(false); clearTimeout(flushTimer); flushTimer = setTimeout(scheduledFlush, FLUSH_MS); }
        },
        sampling: { mousemove: 50, scroll: 150, input: 'last' },
        maskInputOptions: { password: true, email: false, number: false },
        blockClass: 'ct-block',
      });

      flushTimer = setTimeout(scheduledFlush, FLUSH_MS);
      window.addEventListener('beforeunload', function () { sendBatch(true); });
      window._ctFlushReplay = function () { sendBatch(true); };
    }
  }

  // ── Trigger Rules Engine ──────────────────────────────────────────────────
  // Suporta: pageload, click, visibility, scroll, submit.

  (function initRulesEngine() {
    var CACHE_KEY = 'ct_rules_' + apiKey;
    var CACHE_TTL = 5 * 60 * 1000;

    function loadCached() {
      try {
        var raw = localStorage.getItem(CACHE_KEY);
        if (!raw) return null;
        var obj = JSON.parse(raw);
        if (Date.now() - obj.ts > CACHE_TTL) return null;
        return obj.rules;
      } catch (e) { return null; }
    }

    function saveCache(arr) {
      try { localStorage.setItem(CACHE_KEY, JSON.stringify({ ts: Date.now(), rules: arr })); } catch (e) {}
    }

    function fetchRules(cb) {
      var xhr = new XMLHttpRequest();
      xhr.open('GET', apiBase + '/v1/rules', true);
      xhr.setRequestHeader('X-API-Key', apiKey);
      xhr.timeout = 4000;
      xhr.onload = function () {
        try { var d = JSON.parse(xhr.responseText); cb(Array.isArray(d.rules) ? d.rules : []); }
        catch (e) { cb([]); }
      };
      xhr.onerror  = function () { cb([]); };
      xhr.ontimeout = function () { cb([]); };
      xhr.send();
    }

    function loadRules(cb) {
      var cached = loadCached();
      // Só usa cache se não estiver vazio — evita bloquear novas regras
      if (cached && cached.length > 0) {
        cb(cached);
        fetchRules(function (fresh) { saveCache(fresh); }); // refresh em background
        return;
      }
      fetchRules(function (rules) { saveCache(rules); cb(rules); });
    }

    // ── URL matching ───────────────────────────────────────────────────────

    function matchesURL(pattern) {
      if (!pattern || pattern === '*') return true;
      var full = window.location.href;
      var path = window.location.pathname + window.location.search;
      if (pattern.indexOf('contains:') === 0) {
        var needle = pattern.slice(9);
        return path.indexOf(needle) !== -1 || full.indexOf(needle) !== -1;
      }
      try {
        var re = new RegExp('^' + pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*') + '$');
        return re.test(path) || re.test(full);
      } catch (e) { return path.indexOf(pattern) !== -1; }
    }

    // ── Dispatcher ─────────────────────────────────────────────────────────

    function fireRule(rule) {
      var props = Object.assign({}, rule.properties || {}, {
        rule_id:   rule.id,
        rule_name: rule.name,
        url:       window.location.href,
      });
      if (rule.fire_conversion) {
        send('/v1/collect/conversion', {
          session_id: sessionId,
          rule_id:    rule.id,
          event_name: rule.event_name,
          value:      props.value    || 0,
          currency:   props.currency || 'BRL',
        });
      } else {
        window.ConvTrack.track(rule.event_name, props);
      }
    }

    // ── Aplicação das regras ───────────────────────────────────────────────
    //
    // FIX #1 — listeners de click/scroll/submit/visibility são registrados
    // UMA vez por rule.id, não re-adicionados em cada navegação SPA.
    //
    // FIX #5 — listeners de click revalidam matchesURL() a cada disparo.
    //
    // FIX #6 — scroll/visibility têm fired-flag por URL, não por sessão:
    // o fired é resetado quando o pathname muda, permitindo re-disparo.

    var _listenersAdded = {}; // ruleId → true

    function applyRules(rulesList) {
      rulesList.forEach(function (rule) {
        if (!rule.enabled) return;

        switch (rule.type) {

          // pageload: dispara toda vez que a URL bater (incluindo navegações SPA)
          case 'pageload':
            if (matchesURL(rule.url_pattern)) fireRule(rule);
            break;

          // click: listener global adicionado UMA vez; revalida URL em cada clique
          case 'click':
            if (!rule.selector) return;
            if (_listenersAdded[rule.id]) return;
            _listenersAdded[rule.id] = true;
            document.addEventListener('click', function (e) {
              if (!matchesURL(rule.url_pattern)) return; // FIX #5
              try {
                if (e.target.matches(rule.selector) || e.target.closest(rule.selector)) fireRule(rule);
              } catch (ex) {}
            });
            break;

          // visibility: observer adicionado UMA vez; fired-flag por pathname
          case 'visibility':
            if (!rule.selector || !window.IntersectionObserver) return;
            if (_listenersAdded[rule.id]) return;
            _listenersAdded[rule.id] = true;
            (function () {
              var firedOnPath = null; // FIX #6: armazena o pathname onde disparou
              var obs = new IntersectionObserver(function (entries) {
                entries.forEach(function (entry) {
                  if (entry.isIntersecting && matchesURL(rule.url_pattern)) {
                    var curPath = window.location.pathname;
                    if (firedOnPath === curPath) return; // já disparou nesta página
                    firedOnPath = curPath;
                    fireRule(rule);
                  }
                });
              }, { threshold: 0.5 });

              function observeExisting() {
                try { document.querySelectorAll(rule.selector).forEach(function (el) { obs.observe(el); }); } catch (ex) {}
              }

              if (document.readyState === 'loading') {
                document.addEventListener('DOMContentLoaded', observeExisting);
              } else {
                observeExisting();
              }

              // Re-observa elementos após navegação SPA (novo DOM)
              _navListeners.push(function () {
                setTimeout(observeExisting, 100);
              });
            })();
            break;

          // scroll: listener global adicionado UMA vez; fired-flag por pathname
          case 'scroll':
            if (_listenersAdded[rule.id]) return;
            _listenersAdded[rule.id] = true;
            (function () {
              var depth      = rule.scroll_depth || 50;
              var firedOnPath = null; // FIX #6

              function onScroll() {
                if (!matchesURL(rule.url_pattern)) return;
                var curPath = window.location.pathname;
                if (firedOnPath === curPath) return; // já disparou nesta página
                var scrolled = window.scrollY + window.innerHeight;
                var total    = document.documentElement.scrollHeight;
                if (total <= 0) return;
                if ((scrolled / total) * 100 >= depth) {
                  firedOnPath = curPath;
                  fireRule(rule);
                }
              }

              window.addEventListener('scroll', onScroll, { passive: true });
              onScroll(); // verifica posição inicial
            })();
            break;

          // submit: listener global adicionado UMA vez; revalida URL em cada submit
          case 'submit':
            if (_listenersAdded[rule.id]) return;
            _listenersAdded[rule.id] = true;
            (function () {
              var sel = rule.selector || 'form';
              document.addEventListener('submit', function (e) {
                if (!matchesURL(rule.url_pattern)) return; // FIX #5
                try { if (e.target.matches(sel)) fireRule(rule); } catch (ex) {}
              });
            })();
            break;
        }
      });
    }

    // ── Inicialização ──────────────────────────────────────────────────────

    function start() {
      loadRules(applyRules);
    }

    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', start);
    } else {
      start();
    }

    // FIX #2: registra callback no dispatcher unificado (não re-patcha pushState)
    _navListeners.push(function () {
      setTimeout(function () { loadRules(applyRules); }, 50);
    });

  })();

})(window, document);
