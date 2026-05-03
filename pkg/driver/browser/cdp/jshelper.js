window.__maestro = {
  // Tracks how many cross-origin iframes _collectDocs skipped on the most
  // recent walk. Read by the Go side to surface a clear error when a query
  // misses (so users know the cause is OOPIF, not a typo'd selector).
  _lastCrossOriginSkips: 0,

  // Collect the top document plus every same-origin iframe document reachable
  // from it. Cross-origin iframes throw on contentDocument access — we swallow
  // those errors so the query continues across the docs we *can* see, and
  // count them so the caller can warn.
  _collectDocs: function() {
    var docs = [document];
    var skipped = 0;
    function walk(doc) {
      var frames;
      try { frames = doc.querySelectorAll('iframe, frame'); } catch (e) { return; }
      for (var i = 0; i < frames.length; i++) {
        var inner = null;
        try { inner = frames[i].contentDocument; } catch (e) { skipped++; continue; }
        if (inner === null) {
          // Same-origin sandboxed iframe (sandbox without allow-same-origin)
          // also returns null; treat as skipped so the user gets feedback.
          skipped++;
          continue;
        }
        if (docs.indexOf(inner) === -1) {
          docs.push(inner);
          walk(inner);
        }
      }
    }
    walk(document);
    this._lastCrossOriginSkips = skipped;
    return docs;
  },

  // Returns the number of cross-origin / sandboxed frames skipped during the
  // most recent _collectDocs pass. Used by the Go finders to enrich
  // not-found error messages.
  getLastCrossOriginSkips: function() {
    return this._lastCrossOriginSkips || 0;
  },

  findByText: function(text) {
    var lower = text.toLowerCase();
    var docs = this._collectDocs();
    var best = null, bestDepth = -1;
    for (var d = 0; d < docs.length; d++) {
      var all;
      try { all = docs[d].querySelectorAll('*'); } catch (e) { continue; }
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        var t = (el.textContent || '').trim().toLowerCase();
        var label = (el.getAttribute && (el.getAttribute('aria-label') || '')).toLowerCase();
        var ph = (el.getAttribute && (el.getAttribute('placeholder') || '')).toLowerCase();
        if (t.indexOf(lower) !== -1 || label.indexOf(lower) !== -1 || ph.indexOf(lower) !== -1) {
          var depth = 0, n = el;
          while (n.parentElement) { depth++; n = n.parentElement; }
          if (depth > bestDepth) { best = el; bestDepth = depth; }
        }
      }
    }
    if (!best) throw new Error('not found: ' + text);
    var p = best;
    var bestBody = (best.ownerDocument && best.ownerDocument.body) || document.body;
    while (p && p !== bestBody) {
      var tag = p.tagName.toLowerCase();
      if (['a','button','input','select','textarea'].indexOf(tag) !== -1 ||
          p.getAttribute('role') === 'button' || p.getAttribute('tabindex') !== null) return p;
      p = p.parentElement;
    }
    return best;
  },

  // Find first element matching a CSS selector across same-origin frames.
  // Used by Go finders as a fallback when the top-frame Rod lookup fails.
  findByCSSAcrossFrames: function(selector) {
    var docs = this._collectDocs();
    for (var d = 0; d < docs.length; d++) {
      try {
        var el = docs[d].querySelector(selector);
        if (el) return el;
      } catch (e) {}
    }
    throw new Error('not found: ' + selector);
  },

  // Visibility check: returns true if element is visible in the page.
  _isElementVisible: function(el) {
    if (!el || !el.isConnected) return false;
    // Check offsetParent (null means display:none, except for body/html/fixed)
    if (el.offsetParent === null) {
      var style = window.getComputedStyle(el);
      if (style.display === 'none') return false;
      if (style.visibility === 'hidden') return false;
      // Fixed/sticky elements have null offsetParent but can be visible
      if (style.position !== 'fixed' && style.position !== 'sticky') {
        // Check if it's body/html
        var tag = el.tagName.toLowerCase();
        if (tag !== 'body' && tag !== 'html') return false;
      }
    }
    var rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) return false;
    var style = window.getComputedStyle(el);
    if (style.visibility === 'hidden' || style.opacity === '0') return false;
    return true;
  },

  // Find elements matching selector config and return true if any are visible.
  // selectorType: "text", "id", "css", "textContains", "textRegex", or attribute types
  _isAnyVisible: function(selectorType, selectorValue) {
    var self = this;
    var elements = self._findMatchingElements(selectorType, selectorValue);
    for (var i = 0; i < elements.length; i++) {
      if (self._isElementVisible(elements[i])) return true;
    }
    return false;
  },

  // Filter to deepest elements: remove any element that is an ancestor of another match.
  // This ensures text-based visibility checks use the actual text-bearing element,
  // not a parent whose textContent includes hidden children's text.
  _filterToDeepest: function(elements) {
    if (elements.length <= 1) return elements;
    return elements.filter(function(el) {
      for (var i = 0; i < elements.length; i++) {
        if (elements[i] !== el && el.contains(elements[i])) return false;
      }
      return true;
    });
  },

  // Find all elements matching a selector across same-origin docs.
  _findMatchingElements: function(selectorType, selectorValue) {
    var docs = this._collectDocs();
    var results = [];
    function pushAll(nodes) {
      for (var i = 0; i < nodes.length; i++) results.push(nodes[i]);
    }
    var escaped = (selectorValue || '').replace(/"/g, '\\"');

    switch (selectorType) {
      case 'css':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll(selectorValue)); } catch (e) {}
        }
        break;
      case 'id':
        for (var d = 0; d < docs.length; d++) {
          try {
            var el = docs[d].getElementById(selectorValue);
            if (el) results.push(el);
          } catch (e) {}
        }
        break;
      case 'testId':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('[data-testid="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'placeholder':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('[placeholder="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'name':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('[name="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'href':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('a[href*="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'alt':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('[alt="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'title':
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll('[title="' + escaped + '"]')); } catch (e) {}
        }
        break;
      case 'text': {
        var lower = selectorValue.toLowerCase();
        for (var d = 0; d < docs.length; d++) {
          var all;
          try { all = docs[d].querySelectorAll('*'); } catch (e) { continue; }
          for (var i = 0; i < all.length; i++) {
            var el = all[i];
            var t = (el.textContent || '').trim().toLowerCase();
            var label = (el.getAttribute('aria-label') || '').toLowerCase();
            var ph = (el.getAttribute('placeholder') || '').toLowerCase();
            if (t === lower || label === lower || ph === lower ||
                t.indexOf(lower) !== -1 || label.indexOf(lower) !== -1 || ph.indexOf(lower) !== -1) {
              results.push(el);
            }
          }
        }
        results = this._filterToDeepest(results);
        break;
      }
      case 'textContains': {
        var lower = selectorValue.toLowerCase();
        for (var d = 0; d < docs.length; d++) {
          var all;
          try { all = docs[d].querySelectorAll('*'); } catch (e) { continue; }
          for (var i = 0; i < all.length; i++) {
            var t = (all[i].textContent || '').trim().toLowerCase();
            if (t.indexOf(lower) !== -1) results.push(all[i]);
          }
        }
        results = this._filterToDeepest(results);
        break;
      }
      case 'textRegex': {
        try {
          var re = new RegExp(selectorValue, 'i');
          for (var d = 0; d < docs.length; d++) {
            var all;
            try { all = docs[d].querySelectorAll('*'); } catch (e) { continue; }
            for (var i = 0; i < all.length; i++) {
              var t = (all[i].textContent || '').trim();
              var label = all[i].getAttribute('aria-label') || '';
              if (re.test(t) || re.test(label)) results.push(all[i]);
            }
          }
        } catch (e) {}
        results = this._filterToDeepest(results);
        break;
      }
      case 'role': {
        var roleSelector = '[role="' + escaped + '"]';
        for (var d = 0; d < docs.length; d++) {
          try { pushAll(docs[d].querySelectorAll(roleSelector)); } catch (e) {}
        }
        break;
      }
    }
    return results;
  },

  // RAF-based polling: waits until no matching element is visible or timeout.
  // Returns a promise that resolves to true (element gone) or false (still visible at timeout).
  waitForNotVisible: function(selectorType, selectorValue, timeoutMs) {
    var self = this;
    return new Promise(function(resolve) {
      var deadline = Date.now() + timeoutMs;

      // Quick check: already not visible?
      if (!self._isAnyVisible(selectorType, selectorValue)) {
        resolve(true);
        return;
      }

      // RAF polling loop
      function check() {
        if (!self._isAnyVisible(selectorType, selectorValue)) {
          resolve(true);
          return;
        }
        if (Date.now() >= deadline) {
          resolve(false);
          return;
        }
        requestAnimationFrame(check);
      }
      requestAnimationFrame(check);
    });
  },

  // RAF-based polling: waits until a matching element is visible or timeout.
  // Returns a promise that resolves to true (element visible) or false (not found at timeout).
  waitForVisible: function(selectorType, selectorValue, timeoutMs) {
    var self = this;
    return new Promise(function(resolve) {
      var deadline = Date.now() + timeoutMs;

      // Quick check: already visible?
      if (self._isAnyVisible(selectorType, selectorValue)) {
        resolve(true);
        return;
      }

      // RAF polling loop
      function check() {
        if (self._isAnyVisible(selectorType, selectorValue)) {
          resolve(true);
          return;
        }
        if (Date.now() >= deadline) {
          resolve(false);
          return;
        }
        requestAnimationFrame(check);
      }
      requestAnimationFrame(check);
    });
  }
};
