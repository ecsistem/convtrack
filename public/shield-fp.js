/**
 * ConvTrack Shield — Fingerprinting Script v1.0
 * Coleta 13+ sinais do browser para scoring ML anti-bot.
 *
 * Uso: <script src="https://api.seudominio.com/shield-fp.js?k=API_KEY" defer></script>
 */
(function () {
  'use strict';

  // ── Config via query string ────────────────────────────────────────────
  var script = document.currentScript || (function () {
    var all = document.getElementsByTagName('script');
    return all[all.length - 1];
  })();

  var apiBase = '';
  var apiKey  = '';
  var campaignId = '';

  try {
    var u = new URL(script.src);
    apiBase    = u.origin;
    apiKey     = u.searchParams.get('k')  || '';
    campaignId = u.searchParams.get('c')  || '';
  } catch (e) {}

  if (!apiKey || !apiBase) return;

  // ── Detecções imediatas (síncronas) ───────────────────────────────────
  var webdriver    = !!navigator.webdriver;

  // Propriedades injetadas por frameworks de automação (Selenium/Puppeteer/
  // Playwright/PhantomJS/Nightmare). A presença de qualquer uma denuncia o bot.
  function hasAutomationProps() {
    var winProps = [
      '_phantom', '__nightmare', '_selenium', 'callPhantom',
      '_Selenium_IDE_Recorder', 'calledSelenium', '$chrome_asyncScriptInfo',
      '__$webdriverAsyncExecutor', 'domAutomation', 'domAutomationController'
    ];
    for (var i = 0; i < winProps.length; i++) {
      try { if (window[winProps[i]]) return true; } catch (e) {}
    }
    var docKeys = [
      '__webdriver_evaluate', '__selenium_evaluate', '__webdriver_script_function',
      '__webdriver_script_func', '__webdriver_script_fn', '__fxdriver_evaluate',
      '__driver_unwrapped', '__webdriver_unwrapped', '__driver_evaluate',
      '__selenium_unwrapped', '__fxdriver_unwrapped'
    ];
    for (var j = 0; j < docKeys.length; j++) {
      try { if (document[docKeys[j]]) return true; } catch (e) {}
    }
    // ChromeDriver injeta uma chave $cdc_... no document
    try {
      for (var k in document) {
        if (k.indexOf('$cdc_') === 0 || k.indexOf('cdc_adoQpoasnfa76pfcZLmcfl_') === 0) return true;
      }
    } catch (e) {}
    return false;
  }

  var headlessHint = /HeadlessChrome|PhantomJS|Puppeteer|Playwright|Selenium|SlimerJS|CasperJS|Nightmare|Splash|Cypress|TestCafe/i.test(navigator.userAgent)
                   || hasAutomationProps();
  var sessionHash  = (Math.random().toString(36) + Date.now().toString(36)).slice(2, 18);

  // ── Coleta assíncrona de sinais ───────────────────────────────────────

  function simpleHash(str) {
    var h = 0;
    for (var i = 0; i < str.length; i++) {
      h = (Math.imul(31, h) + str.charCodeAt(i)) | 0;
    }
    return (h >>> 0).toString(16);
  }

  // 1. Canvas fingerprint
  function getCanvasHash() {
    try {
      var c = document.createElement('canvas');
      c.width = 220; c.height = 30;
      var ctx = c.getContext('2d');
      ctx.textBaseline = 'top';
      ctx.font = '14px Arial';
      ctx.fillStyle = '#f60';
      ctx.fillRect(125, 1, 62, 20);
      ctx.fillStyle = '#069';
      ctx.fillText('ConvTrack Shield 🔒', 2, 15);
      ctx.fillStyle = 'rgba(102,204,0,0.7)';
      ctx.fillText('ConvTrack Shield 🔒', 4, 17);
      return simpleHash(c.toDataURL());
    } catch (e) { return 'error'; }
  }

  // 2. WebGL fingerprint
  function getWebGLInfo() {
    try {
      var c = document.createElement('canvas');
      var gl = c.getContext('webgl') || c.getContext('experimental-webgl');
      if (!gl) return { vendor: 'none', renderer: 'none', hash: 'none' };
      var ext = gl.getExtension('WEBGL_debug_renderer_info');
      var vendor   = ext ? gl.getParameter(ext.UNMASKED_VENDOR_WEBGL)   : gl.getParameter(gl.VENDOR);
      var renderer = ext ? gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER);
      // Draw basic scene for hash
      var prog = gl.createProgram();
      return {
        vendor:   vendor   || 'unknown',
        renderer: renderer || 'unknown',
        hash:     simpleHash(vendor + '|' + renderer),
      };
    } catch (e) { return { vendor: 'error', renderer: 'error', hash: 'error' }; }
  }

  // 3. Audio fingerprint (AudioContext)
  function getAudioHash(cb) {
    try {
      var AudioCtx = window.OfflineAudioContext || window.webkitOfflineAudioContext;
      if (!AudioCtx) { cb('none'); return; }
      var ctx = new AudioCtx(1, 44100, 44100);
      var osc = ctx.createOscillator();
      var comp = ctx.createDynamicsCompressor();
      comp.threshold.value = -50; comp.knee.value = 40;
      comp.ratio.value = 12; comp.reduction.value = -20;
      comp.attack.value = 0; comp.release.value = 0.25;
      osc.type = 'triangle';
      osc.frequency.value = 10000;
      osc.connect(comp);
      comp.connect(ctx.destination);
      osc.start(0);
      ctx.oncomplete = function (e) {
        var buf = e.renderedBuffer.getChannelData(0);
        var sum = 0;
        for (var i = 4500; i < 5000; i++) sum += Math.abs(buf[i]);
        cb(simpleHash(sum.toString()));
      };
      ctx.startRendering();
    } catch (e) { cb('error'); }
  }

  // 4. WebRTC local IP leak
  function getWebRTCIPs(cb) {
    try {
      var ips = [];
      var pc = new (window.RTCPeerConnection || window.mozRTCPeerConnection ||
                    window.webkitRTCPeerConnection)({ iceServers: [] });
      pc.createDataChannel('');
      pc.createOffer().then(function (o) { return pc.setLocalDescription(o); });
      pc.onicecandidate = function (e) {
        if (!e || !e.candidate) { pc.close(); cb(ips); return; }
        var m = /([0-9]{1,3}(\.[0-9]{1,3}){3})/.exec(e.candidate.candidate);
        if (m && ips.indexOf(m[1]) === -1) ips.push(m[1]);
      };
      setTimeout(function () { pc.close(); cb(ips); }, 500);
    } catch (e) { cb([]); }
  }

  // 5. Fonts fingerprint (detecta fontes instaladas por métricas)
  function getFontsHash() {
    try {
      var baseFonts = ['monospace', 'sans-serif', 'serif'];
      var testFonts = ['Arial', 'Courier New', 'Georgia', 'Helvetica',
                       'Impact', 'Tahoma', 'Times New Roman', 'Trebuchet MS',
                       'Verdana', 'Comic Sans MS', 'Palatino', 'Garamond',
                       'Calibri', 'Segoe UI', 'Roboto', 'Open Sans'];
      var testStr = 'mmmmmmmmmmlli';
      var testSize = '72px';

      var span = document.createElement('span');
      span.style.cssText = 'position:absolute;visibility:hidden;font-size:' + testSize;
      span.innerHTML = testStr;
      document.body.appendChild(span);

      var baseSizes = {};
      baseFonts.forEach(function (f) {
        span.style.fontFamily = f;
        baseSizes[f] = { w: span.offsetWidth, h: span.offsetHeight };
      });

      var detected = [];
      testFonts.forEach(function (font) {
        for (var i = 0; i < baseFonts.length; i++) {
          span.style.fontFamily = "'" + font + "'," + baseFonts[i];
          if (span.offsetWidth !== baseSizes[baseFonts[i]].w ||
              span.offsetHeight !== baseSizes[baseFonts[i]].h) {
            detected.push(font);
            break;
          }
        }
      });
      document.body.removeChild(span);
      return simpleHash(detected.join(','));
    } catch (e) { return 'error'; }
  }

  // ── Coleta e envio ─────────────────────────────────────────────────────

  var webgl = getWebGLInfo();

  getAudioHash(function (audioHash) {
    getWebRTCIPs(function (webrtcIPs) {
      var payload = {
        api_key:       apiKey,
        campaign_id:   campaignId,
        session_hash:  sessionHash,

        // Sinais client-side
        webdriver:     webdriver,
        headless_hint: headlessHint,
        devtools:      false, // será atualizado abaixo se detectado

        // Canvas
        canvas_hash:   getCanvasHash(),

        // WebGL
        webgl_vendor:   webgl.vendor,
        webgl_renderer: webgl.renderer,
        webgl_hash:     webgl.hash,

        // Audio
        audio_hash: audioHash,

        // Screen
        screen_width:  screen.width  || 0,
        screen_height: screen.height || 0,
        color_depth:   screen.colorDepth || 0,
        pixel_ratio:   window.devicePixelRatio || 1,

        // Browser/device
        timezone:     (Intl && Intl.DateTimeFormat) ? Intl.DateTimeFormat().resolvedOptions().timeZone : '',
        language:     navigator.language || '',
        platform:     navigator.platform || '',
        cpu_cores:    navigator.hardwareConcurrency || 0,
        memory_gb:    navigator.deviceMemory        || 0,
        touch_points: navigator.maxTouchPoints      || 0,
        plugins:      navigator.plugins ? navigator.plugins.length : 0,

        // WebRTC IPs
        webrtc_ips: webrtcIPs,

        // Fonts
        fonts_hash: getFontsHash(),
      };

      // Detecção de DevTools (tamanho de janela)
      var devToolsOpen = (window.outerWidth - window.innerWidth > 160) ||
                         (window.outerHeight - window.innerHeight > 160);
      payload.devtools = devToolsOpen;

      // Envia ao servidor
      var endpoint = apiBase + '/v1/shield/fingerprint';
      if (navigator.sendBeacon) {
        navigator.sendBeacon(endpoint, JSON.stringify(payload));
      } else {
        var xhr = new XMLHttpRequest();
        xhr.open('POST', endpoint, true);
        xhr.setRequestHeader('Content-Type', 'application/json');
        xhr.setRequestHeader('X-API-Key', apiKey);
        xhr.send(JSON.stringify(payload));
      }
    });
  });

})();
