(function (window, document) {
  'use strict';

  var STORAGE_VISITOR = 'ct_vid';
  var STORAGE_SESSION = 'ct_sid';
  var STORAGE_ATTR    = 'ct_attr';
  var SESSION_TIMEOUT = 30 * 60 * 1000; // 30 min inactivity = new session

  var script   = document.currentScript || (function () {
    var scripts = document.getElementsByTagName('script');
    return scripts[scripts.length - 1];
  })();

  var apiKey   = script.getAttribute('data-key') || '';
  var apiBase  = script.getAttribute('data-api') || (function () {
    try { return new URL(script.src).origin; } catch (e) { return ''; }
  })();
  var replayOn = script.getAttribute('data-replay') !== 'false';

  if (!apiKey) {
    console.warn('[ConvTrack] Missing data-key attribute.');
    return;
  }

  // ── Utilities ─────────────────────────────────────────────────────────────

  function uuid() {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) {
      return crypto.randomUUID();
    }
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
    try {
      var v = localStorage.getItem(key);
      if (v) return v;
    } catch (e) {}
    try {
      var match = document.cookie.match(new RegExp('(?:^|;)\\s*' + key + '=([^;]+)'));
      return match ? decodeURIComponent(match[1]) : null;
    } catch (e) { return null; }
  }

  function getCookie(name) {
    var match = document.cookie.match(new RegExp('(?:^|;)\\s*' + name + '=([^;]+)'));
    return match ? decodeURIComponent(match[1]) : '';
  }

  function getParam(name) {
    try {
      return new URLSearchParams(window.location.search).get(name) || '';
    } catch (e) {
      var match = window.location.search.match(new RegExp('[?&]' + name + '=([^&]+)'));
      return match ? decodeURIComponent(match[1]) : '';
    }
  }

  function send(path, body) {
    var url = apiBase + path;
    var data = JSON.stringify(Object.assign({ api_key: apiKey }, body));
    if (navigator.sendBeacon) {
      var blob = new Blob([data], { type: 'application/json' });
      navigator.sendBeacon(url, blob);
    } else {
      var xhr = new XMLHttpRequest();
      xhr.open('POST', url, true);
      xhr.setRequestHeader('Content-Type', 'application/json');
      xhr.setRequestHeader('X-API-Key', apiKey);
      xhr.send(data);
    }
  }

  // ── Visitor & Session ─────────────────────────────────────────────────────

  var visitorId = load(STORAGE_VISITOR);
  if (!visitorId) {
    visitorId = uuid();
    store(STORAGE_VISITOR, visitorId);
  }

  function needsNewSession() {
    var ts = load(STORAGE_SESSION + '_ts');
    if (!ts) return true;
    return Date.now() - parseInt(ts, 10) > SESSION_TIMEOUT;
  }

  var sessionId = load(STORAGE_SESSION);
  if (!sessionId || needsNewSession()) {
    sessionId = uuid();
    store(STORAGE_SESSION, sessionId);
  }
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

  // Persist attribution if new params found; otherwise load stored
  if (hasNewAttribution) {
    store(STORAGE_ATTR, JSON.stringify(attr));
  } else {
    try { attr = JSON.parse(load(STORAGE_ATTR) || '{}'); } catch (e) {}
  }

  // FBP & FBC cookies
  attr.fbp = getCookie('_fbp');
  attr.fbc  = getCookie('_fbc');
  if (!attr.fbc && attr.fbclid) {
    attr.fbc = 'fb.1.' + Date.now() + '.' + attr.fbclid;
  }

  // ── Device & browser metadata ─────────────────────────────────────────────

  var screenW  = window.screen ? (window.screen.width  || 0) : 0;
  var screenH  = window.screen ? (window.screen.height || 0) : 0;
  var timezone = '';
  try { timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (e) {}
  var language = navigator.language || navigator.userLanguage || '';

  // ── Engagement / duration tracking ───────────────────────────────────────

  var sessionStartTs  = Date.now();
  var engagedMs       = 0;
  var engagedStart    = null;  // timestamp when user became active
  var pageCount       = 1;
  var currentPage     = window.location.href;
  var heartbeatTimer  = null;
  var HEARTBEAT_MS    = 30 * 1000; // 30 segundos

  function isActive() { return document.visibilityState !== 'hidden'; }

  function startEngaged() {
    if (engagedStart === null && isActive()) engagedStart = Date.now();
  }
  function pauseEngaged() {
    if (engagedStart !== null) {
      engagedMs  += Date.now() - engagedStart;
      engagedStart = null;
    }
  }

  startEngaged();
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') { pauseEngaged(); } else { startEngaged(); }
  });
  window.addEventListener('focus', startEngaged);
  window.addEventListener('blur',  pauseEngaged);

  function totalDurationSeconds() {
    return Math.round((Date.now() - sessionStartTs) / 1000);
  }

  function sendHeartbeat(isFinal) {
    pauseEngaged();
    if (isFinal && !isActive()) startEngaged(); // resume tracker before final flush
    var body = {
      session_id:       sessionId,
      duration_seconds: totalDurationSeconds(),
      page_count:       pageCount,
      current_page:     currentPage,
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
    heartbeatTimer = setTimeout(function () {
      sendHeartbeat(false);
      scheduleHeartbeat();
    }, HEARTBEAT_MS);
  }

  scheduleHeartbeat();
  window.addEventListener('beforeunload', function () { sendHeartbeat(true); });

  // ── Send session to API ───────────────────────────────────────────────────
  // Usa fetch (não sendBeacon) para poder ler a resposta e detectar clone.

  function collectSession() {
    var url  = apiBase + '/v1/collect/session';
    var body = JSON.stringify(Object.assign({ api_key: apiKey }, {
      visitor_id:    visitorId,
      session_id:    sessionId,
      landing_page:  window.location.href,
      referrer:      document.referrer,
      user_agent:    navigator.userAgent,
      screen_width:  screenW,
      screen_height: screenH,
      timezone:      timezone,
      language:      language,
    }, attr));

    fetch(url, {
      method:    'POST',
      headers:   { 'Content-Type': 'application/json', 'X-API-Key': apiKey },
      body:      body,
      keepalive: true,
    })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (data && data.clone_detected && data.redirect_url) {
          window.location.replace(data.redirect_url);
        }
      })
      .catch(function () {});
  }

  collectSession();

  // ── Public API ────────────────────────────────────────────────────────────

  window.ConvTrack = {
    track: function (eventName, properties) {
      send('/v1/collect/event', {
        session_id:  sessionId,
        name:        eventName,
        properties:  properties || {},
      });
    },

    identify: function (email, phone) {
      send('/v1/collect/identify', {
        visitor_id: visitorId,
        session_id: sessionId,
        email:      email || '',
        phone:      phone || '',
      });
    },

    getSessionId:  function () { return sessionId; },
    getVisitorId:  function () { return visitorId; },
    getAttribution: function () { return Object.assign({}, attr); },
  };

  // ── Visual Inspector ─────────────────────────────────────────────────────
  // Ativado via ?tracker_inspect=1 na URL.
  // Permite ao usuário clicar em qualquer elemento para obter o seletor CSS.

  (function initInspector() {
    if (!getParam('tracker_inspect')) return;

    // ── Estilos injetados ──────────────────────────────────────────────────
    var style = document.createElement('style');
    style.textContent = [
      '#ct-inspector-banner{',
        'all:initial;',
        'position:fixed;top:0;left:0;right:0;z-index:2147483647;',
        'background:#4f46e5;color:#fff;',
        'font:600 13px/1 -apple-system,sans-serif;',
        'padding:10px 16px;',
        'display:flex;align-items:center;gap:12px;',
        'box-shadow:0 2px 12px rgba(0,0,0,.35);',
      '}',
      '#ct-inspector-banner span{flex:1}',
      '#ct-inspector-banner kbd{',
        'background:rgba(255,255,255,.15);border-radius:4px;',
        'padding:2px 6px;font:inherit;font-size:11px;',
      '}',
      '#ct-inspector-banner button{',
        'background:rgba(255,255,255,.15);border:none;color:#fff;',
        'cursor:pointer;border-radius:6px;padding:4px 10px;font:inherit;',
      '}',
      '#ct-inspector-banner button:hover{background:rgba(255,255,255,.3)}',
      '#ct-inspector-highlight{',
        'position:fixed;pointer-events:none;z-index:2147483646;',
        'border:2px solid #4f46e5;border-radius:3px;',
        'background:rgba(79,70,229,.08);',
        'transition:all .08s ease;',
        'box-shadow:0 0 0 9999px rgba(0,0,0,.18);',
      '}',
      '#ct-inspector-tooltip{',
        'position:fixed;z-index:2147483647;pointer-events:none;',
        'background:#1e1b4b;color:#c7d2fe;',
        'font:500 11px/1.4 monospace;',
        'padding:4px 8px;border-radius:5px;',
        'white-space:nowrap;max-width:340px;overflow:hidden;text-overflow:ellipsis;',
        'box-shadow:0 2px 8px rgba(0,0,0,.4);',
      '}',
      '#ct-inspector-panel{',
        'position:fixed;bottom:0;left:0;right:0;z-index:2147483647;',
        'background:#1e1b4b;color:#e0e7ff;',
        'font:13px/1.6 -apple-system,sans-serif;',
        'padding:16px 20px 20px;',
        'box-shadow:0 -4px 20px rgba(0,0,0,.4);',
        'border-top:1px solid #3730a3;',
        'transform:translateY(100%);transition:transform .2s ease;',
      '}',
      '#ct-inspector-panel.ct-open{transform:translateY(0)}',
      '#ct-inspector-panel h3{',
        'margin:0 0 10px;font-size:12px;text-transform:uppercase;',
        'letter-spacing:.08em;color:#818cf8;',
      '}',
      '.ct-panel-row{display:flex;gap:8px;margin-bottom:8px;align-items:flex-start}',
      '.ct-panel-row label{',
        'font-size:11px;color:#818cf8;min-width:80px;padding-top:4px;flex-shrink:0',
      '}',
      '.ct-code{',
        'flex:1;background:#312e81;border-radius:6px;',
        'padding:7px 10px;font:12px/1.5 monospace;color:#c7d2fe;',
        'word-break:break-all;border:1px solid #3730a3;',
      '}',
      '.ct-copy-btn{',
        'flex-shrink:0;background:#4f46e5;border:none;color:#fff;',
        'cursor:pointer;border-radius:6px;padding:6px 10px;font-size:12px;',
        'white-space:nowrap;',
      '}',
      '.ct-copy-btn:hover{background:#4338ca}',
      '.ct-copy-btn.ct-copied{background:#059669}',
      '.ct-close-panel{',
        'float:right;background:none;border:none;color:#818cf8;',
        'cursor:pointer;font-size:18px;line-height:1;padding:0 0 4px 8px;',
      '}',
    ].join('');
    document.head.appendChild(style);

    // ── Banner ─────────────────────────────────────────────────────────────
    var banner = document.createElement('div');
    banner.id = 'ct-inspector-banner';
    banner.innerHTML = [
      '<span>🔍 <strong>ConvTrack Inspector</strong> — Passe o mouse e clique no elemento que quer rastrear</span>',
      '<kbd>Esc</kbd> para sair',
      '<button id="ct-exit-btn">✕ Sair</button>',
    ].join('');
    document.body.appendChild(banner);

    // Empurra o conteúdo da página pra baixo do banner
    var bannerH = banner.offsetHeight || 44;
    document.body.style.marginTop = (parseInt(document.body.style.marginTop || 0) + bannerH) + 'px';

    // ── Highlight ──────────────────────────────────────────────────────────
    var highlight = document.createElement('div');
    highlight.id = 'ct-inspector-highlight';
    highlight.style.display = 'none';
    document.body.appendChild(highlight);

    var tooltip = document.createElement('div');
    tooltip.id = 'ct-inspector-tooltip';
    tooltip.style.display = 'none';
    document.body.appendChild(tooltip);

    // ── Painel ─────────────────────────────────────────────────────────────
    var panel = document.createElement('div');
    panel.id = 'ct-inspector-panel';
    panel.innerHTML = '<button class="ct-close-panel" id="ct-close-panel">✕</button><h3>Elemento selecionado</h3><div id="ct-panel-body"></div>';
    document.body.appendChild(panel);

    // ── Gerador de seletor CSS ──────────────────────────────────────────────
    function cssEscape(s) {
      return typeof CSS !== 'undefined' && CSS.escape
        ? CSS.escape(s)
        : s.replace(/([^\w-])/g, '\\$1');
    }

    function getSelector(el) {
      // 1. ID único
      if (el.id && document.querySelectorAll('#' + cssEscape(el.id)).length === 1) {
        return '#' + cssEscape(el.id);
      }

      var path = [];
      var cur = el;

      while (cur && cur !== document.body && cur !== document.documentElement) {
        var tag = cur.tagName.toLowerCase();
        var part = tag;

        // ID
        if (cur.id) {
          part = '#' + cssEscape(cur.id);
          path.unshift(part);
          break;
        }

        // Classes úteis (ignora classes geradas: hash-like com +8 chars sem hífen)
        var goodClasses = [];
        for (var i = 0; i < cur.classList.length; i++) {
          var c = cur.classList[i];
          if (!/^[a-z0-9]{8,}$/.test(c) && !/^[A-Z]/.test(c)) {
            goodClasses.push('.' + cssEscape(c));
            if (goodClasses.length >= 2) break;
          }
        }
        if (goodClasses.length) {
          part = tag + goodClasses.join('');
        }

        // nth-child para desambiguar irmãos com mesmo seletor
        var parent = cur.parentElement;
        if (parent) {
          var same = Array.prototype.filter.call(parent.children, function (s) {
            return s.tagName === cur.tagName;
          });
          if (same.length > 1) {
            var idx = Array.prototype.indexOf.call(parent.children, cur) + 1;
            part += ':nth-child(' + idx + ')';
          }
        }

        path.unshift(part);

        // Para se o seletor já é único
        try {
          if (document.querySelectorAll(path.join(' > ')).length === 1) break;
        } catch (e) { break; }

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

    // ── Hover ─────────────────────────────────────────────────────────────
    var lastTarget = null;

    function onMouseMove(e) {
      var el = e.target;
      if (el === banner || banner.contains(el) ||
          el === panel  || panel.contains(el) ||
          el === highlight || el === tooltip) return;

      if (el === lastTarget) return;
      lastTarget = el;

      var r = el.getBoundingClientRect();
      highlight.style.display = 'block';
      highlight.style.top     = r.top    + 'px';
      highlight.style.left    = r.left   + 'px';
      highlight.style.width   = r.width  + 'px';
      highlight.style.height  = r.height + 'px';

      // Tooltip com nome do elemento
      tooltip.style.display = 'block';
      tooltip.textContent   = getTagInfo(el);

      // Posiciona tooltip abaixo do elemento (ou acima se não tiver espaço)
      var tx = r.left;
      var ty = r.bottom + 6;
      if (ty + 30 > window.innerHeight) ty = r.top - 30;
      tooltip.style.top  = ty + 'px';
      tooltip.style.left = tx + 'px';
    }

    // ── Click ─────────────────────────────────────────────────────────────
    function onClickCapture(e) {
      var el = e.target;
      // Deixa o botão de sair funcionar
      if (el === document.getElementById('ct-exit-btn') ||
          el === document.getElementById('ct-close-panel')) return;
      if (banner.contains(el) || panel.contains(el)) return;

      e.preventDefault();
      e.stopPropagation();

      var selector = getSelector(el);
      var snippet  = 'document.querySelector(\'' + selector + '\')\n' +
                     '  .addEventListener(\'click\', function () {\n' +
                     '    ConvTrack.track(\'meu_evento\', { page: location.href });\n' +
                     '  });';

      showPanel(selector, snippet, el);
    }

    function copyText(text, btn) {
      var prev = btn.textContent;
      btn.classList.add('ct-copied');
      btn.textContent = '✓ Copiado!';
      setTimeout(function () {
        btn.classList.remove('ct-copied');
        btn.textContent = prev;
      }, 2000);

      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text);
      } else {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.cssText = 'position:fixed;left:-9999px';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
    }

    function showPanel(selector, snippet, el) {
      var body = document.getElementById('ct-panel-body');
      body.innerHTML = [
        '<div class="ct-panel-row">',
          '<label>Seletor</label>',
          '<code class="ct-code" id="ct-sel-code">', escHtml(selector), '</code>',
          '<button class="ct-copy-btn" id="ct-copy-sel">Copiar</button>',
        '</div>',
        '<div class="ct-panel-row">',
          '<label>Snippet</label>',
          '<code class="ct-code" style="white-space:pre">', escHtml(snippet), '</code>',
          '<button class="ct-copy-btn" id="ct-copy-snip">Copiar</button>',
        '</div>',
      ].join('');
      panel.classList.add('ct-open');

      document.getElementById('ct-copy-sel').onclick = function () {
        copyText(selector, this);
      };
      document.getElementById('ct-copy-snip').onclick = function () {
        copyText(snippet, this);
      };

      // Copia o seletor automaticamente
      copyText(selector, document.getElementById('ct-copy-sel'));
    }

    function escHtml(s) {
      return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
    }

    // ── Destruição ─────────────────────────────────────────────────────────
    function exitInspector() {
      document.removeEventListener('mousemove', onMouseMove);
      document.removeEventListener('click', onClickCapture, true);
      document.removeEventListener('keydown', onKeyDown);
      if (banner.parentNode)    banner.parentNode.removeChild(banner);
      if (highlight.parentNode) highlight.parentNode.removeChild(highlight);
      if (tooltip.parentNode)   tooltip.parentNode.removeChild(tooltip);
      if (panel.parentNode)     panel.parentNode.removeChild(panel);
      document.body.style.marginTop = '';
      // Remove o parâmetro da URL sem recarregar a página
      var url = new URL(window.location.href);
      url.searchParams.delete('tracker_inspect');
      history.replaceState(null, '', url.toString());
    }

    function onKeyDown(e) {
      if (e.key === 'Escape') exitInspector();
    }

    document.getElementById('ct-exit-btn').addEventListener('click', exitInspector);
    document.getElementById('ct-close-panel').addEventListener('click', function () {
      panel.classList.remove('ct-open');
    });

    document.addEventListener('mousemove', onMouseMove);
    document.addEventListener('click', onClickCapture, true); // capture para interceptar links/forms
    document.addEventListener('keydown', onKeyDown);

  })();

  // ── Auto pageview on SPA navigation ──────────────────────────────────────

  (function () {
    var lastPath = window.location.pathname;
    var origPush = history.pushState;
    var origReplace = history.replaceState;

    function onNav() {
      if (window.location.pathname !== lastPath) {
        lastPath    = window.location.pathname;
        currentPage = window.location.href;
        pageCount  += 1;
        store(STORAGE_SESSION + '_ts', String(Date.now()));
        window.ConvTrack.track('pageview', { url: window.location.href });
        // Envia heartbeat imediato com página atualizada
        sendHeartbeat(false);
        scheduleHeartbeat();
      }
    }

    history.pushState = function () { origPush.apply(history, arguments); onNav(); };
    history.replaceState = function () { origReplace.apply(history, arguments); onNav(); };
    window.addEventListener('popstate', onNav);
  })();

  // ── Session Replay (rrweb) — only when on checkout/lead pages ────────────

  if (replayOn) {
    var replayTriggers = (script.getAttribute('data-replay-triggers') || 'checkout,order,obrigado,thank,lead').split(',');
    var currentPath = window.location.pathname.toLowerCase();
    var shouldRecord = replayTriggers.some(function (t) { return currentPath.indexOf(t.trim()) !== -1; });

    // Detecta qual trigger gerou a gravação
    var replayTrigger = '';
    replayTriggers.forEach(function (t) {
      if (currentPath.indexOf(t.trim()) !== -1) replayTrigger = t.trim();
    });

    if (shouldRecord && typeof rrweb !== 'undefined') {
      var replayBatch = [];
      var flushTimer;
      var BATCH_SIZE = 50;   // envia a cada 50 eventos ou 5 segundos
      var FLUSH_MS   = 5000;

      function sendBatch(final) {
        if (replayBatch.length === 0) {
          // Se não há eventos pendentes mas é final, ainda manda o flush
          if (!final) return;
        }
        var toSend = replayBatch.splice(0, replayBatch.length);

        if (final) {
          // Primeiro envia eventos pendentes (se houver), depois sinaliza flush S3
          if (toSend.length > 0) {
            send('/v1/replay/events', {
              session_id: sessionId,
              trigger:    replayTrigger,
              events:     toSend,
            });
          }
          // Sinaliza backend para mover Redis → S3 (independente de ter eventos)
          var flushBody = JSON.stringify({
            api_key:    apiKey,
            session_id: sessionId,
            trigger:    replayTrigger,
          });
          if (navigator.sendBeacon) {
            navigator.sendBeacon(
              apiBase + '/v1/replay/flush',
              new Blob([flushBody], { type: 'application/json' })
            );
          } else {
            send('/v1/replay/flush', { session_id: sessionId, trigger: replayTrigger });
          }
        } else {
          send('/v1/replay/events', {
            session_id: sessionId,
            trigger:    replayTrigger,
            events:     toSend,
          });
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
          if (replayBatch.length >= BATCH_SIZE) {
            sendBatch(false);
            clearTimeout(flushTimer);
            flushTimer = setTimeout(scheduledFlush, FLUSH_MS);
          }
        },
        sampling: {
          mousemove: 50,
          scroll:    150,
          input:     'last',
        },
        maskInputOptions: {
          password: true,
          email:    false,
          number:   false,
        },
        blockClass: 'ct-block',
      });

      flushTimer = setTimeout(scheduledFlush, FLUSH_MS);

      // Flush final quando o usuário sai da página
      window.addEventListener('beforeunload', function () {
        sendBatch(true); // envia pendentes + sinaliza flush Redis → S3
      });

      // Expõe flush manual para testes (test.html usa isso)
      window._ctFlushReplay = function () { sendBatch(true); };
    }
  }

  // ── Trigger Rules Engine ──────────────────────────────────────────────────
  // Busca as regras do projeto via API e aplica na página automaticamente.
  // Suporta: pageload, click, visibility, scroll, submit.

  (function initRulesEngine() {
    var CACHE_KEY = 'ct_rules_' + apiKey;
    var CACHE_TTL = 5 * 60 * 1000; // 5 minutos

    // ── Cache ──────────────────────────────────────────────────────────────
    function loadCached() {
      try {
        var raw = localStorage.getItem(CACHE_KEY);
        if (!raw) return null;
        var obj = JSON.parse(raw);
        if (Date.now() - obj.ts > CACHE_TTL) return null;
        return obj.rules;
      } catch (e) { return null; }
    }

    function saveCache(rulesArr) {
      try {
        localStorage.setItem(CACHE_KEY, JSON.stringify({ ts: Date.now(), rules: rulesArr }));
      } catch (e) {}
    }

    function fetchRules(cb) {
      var xhr = new XMLHttpRequest();
      xhr.open('GET', apiBase + '/v1/rules', true);
      xhr.setRequestHeader('X-API-Key', apiKey);
      xhr.timeout = 4000;
      xhr.onload = function () {
        try {
          var data = JSON.parse(xhr.responseText);
          cb(Array.isArray(data.rules) ? data.rules : []);
        } catch (e) { cb([]); }
      };
      xhr.onerror  = function () { cb([]); };
      xhr.ontimeout = function () { cb([]); };
      xhr.send();
    }

    function loadRules(cb) {
      var cached = loadCached();
      if (cached) {
        cb(cached);
        // Refresh em background para próxima visita
        fetchRules(function (fresh) { saveCache(fresh); });
        return;
      }
      fetchRules(function (rules) {
        saveCache(rules);
        cb(rules);
      });
    }

    // ── URL Matching ───────────────────────────────────────────────────────
    // Padrões suportados:
    //   *              → qualquer URL
    //   /obrigado      → pathname exato
    //   /checkout*     → começa com
    //   contains:texto → URL contém o texto
    //   */obrigado     → glob
    function matchesURL(pattern) {
      if (!pattern || pattern === '*') return true;
      var full = window.location.href;
      var path = window.location.pathname + window.location.search;

      if (pattern.indexOf('contains:') === 0) {
        return path.indexOf(pattern.slice(9)) !== -1 ||
               full.indexOf(pattern.slice(9)) !== -1;
      }

      // Converte glob (* → .*) para RegExp
      try {
        var re = new RegExp('^' + pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*') + '$');
        return re.test(path) || re.test(full);
      } catch (e) {
        return path.indexOf(pattern) !== -1;
      }
    }

    // ── Dispatcher ─────────────────────────────────────────────────────────
    function fireRule(rule, el) {
      var props = Object.assign({}, rule.properties || {}, {
        rule_id:   rule.id,
        rule_name: rule.name,
        url:       window.location.href,
      });

      if (rule.fire_conversion) {
        // Disparo server-side → aciona CAPI / TikTok / Kwai / Google via fila
        send('/v1/collect/conversion', {
          session_id: sessionId,
          rule_id:    rule.id,
          event_name: rule.event_name,
          value:      props.value || 0,
          currency:   props.currency || 'BRL',
        });
      } else {
        // Disparo client-side simples
        window.ConvTrack.track(rule.event_name, props);
      }
    }

    // ── Aplicação das regras ───────────────────────────────────────────────
    function applyRules(rulesList) {
      var scrollFired = {}; // { ruleId: true } para evitar duplicação

      rulesList.forEach(function (rule) {
        if (!rule.enabled) return;
        if (!matchesURL(rule.url_pattern)) return;

        switch (rule.type) {

          // ── pageload ─────────────────────────────────────────────────────
          case 'pageload':
            fireRule(rule);
            break;

          // ── click ────────────────────────────────────────────────────────
          case 'click':
            if (!rule.selector) return;
            document.addEventListener('click', function (e) {
              var el = e.target;
              try {
                if (el.matches(rule.selector) || el.closest(rule.selector)) {
                  fireRule(rule, el);
                }
              } catch (ex) {}
            });
            break;

          // ── visibility (IntersectionObserver) ────────────────────────────
          case 'visibility':
            if (!rule.selector || !window.IntersectionObserver) return;
            (function () {
              var fired = false;
              var obs = new IntersectionObserver(function (entries) {
                entries.forEach(function (entry) {
                  if (!fired && entry.isIntersecting) {
                    fired = true;
                    fireRule(rule, entry.target);
                    obs.unobserve(entry.target);
                  }
                });
              }, { threshold: 0.5 });

              // Tenta observar elementos já no DOM e também novos (SPA)
              function observeExisting() {
                try {
                  document.querySelectorAll(rule.selector).forEach(function (el) {
                    obs.observe(el);
                  });
                } catch (ex) {}
              }

              if (document.readyState === 'loading') {
                document.addEventListener('DOMContentLoaded', observeExisting);
              } else {
                observeExisting();
              }
            })();
            break;

          // ── scroll ───────────────────────────────────────────────────────
          case 'scroll':
            (function () {
              var depth = rule.scroll_depth || 50;
              var fired = false;
              function onScroll() {
                if (fired) return;
                var scrolled = window.scrollY + window.innerHeight;
                var total    = document.documentElement.scrollHeight;
                if (total <= 0) return;
                var pct = (scrolled / total) * 100;
                if (pct >= depth) {
                  fired = true;
                  scrollFired[rule.id] = true;
                  window.removeEventListener('scroll', onScroll);
                  fireRule(rule);
                }
              }
              window.addEventListener('scroll', onScroll, { passive: true });
              onScroll(); // verifica posição inicial (página curta ou scroll restaurado)
            })();
            break;

          // ── submit ───────────────────────────────────────────────────────
          case 'submit':
            (function () {
              var sel = rule.selector || 'form';
              document.addEventListener('submit', function (e) {
                try {
                  if (e.target.matches(sel)) fireRule(rule, e.target);
                } catch (ex) {}
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

    // Re-aplica regras em navegações SPA (após pushState/replaceState)
    var _origPush    = history.pushState;
    var _origReplace = history.replaceState;
    function onSPANav() {
      // Aguarda um tick para o DOM atualizar
      setTimeout(function () { loadRules(applyRules); }, 50);
    }
    history.pushState    = function () { _origPush.apply(history, arguments);    onSPANav(); };
    history.replaceState = function () { _origReplace.apply(history, arguments); onSPANav(); };

  })();

})(window, document);
