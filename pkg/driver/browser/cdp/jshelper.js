window.__maestro = {
  // Tracks how many cross-origin iframes _collectRoots skipped on the most
  // recent walk. Read by the Go side to surface a clear error when a query
  // misses (so users know the cause is OOPIF, not a typo'd selector).
  _lastCrossOriginSkips: 0,

  // Collect every same-origin DOM root reachable from the top frame:
  //   * the top document
  //   * same-origin iframe / frame contentDocuments (recursively)
  //   * any open shadowRoot (mode: "open") attached to any element in any
  //     of the above (recursively)
  //
  // Document and ShadowRoot share the relevant DOM API surface
  // (querySelectorAll / getElementById), so callers can treat the returned
  // list uniformly.
  //
  // Cross-origin iframes throw on contentDocument access; we swallow and
  // count them so the caller can warn. Closed shadow roots
  // (`mode: "closed"`) return null from `el.shadowRoot` and are not
  // detectable from outside — same constraint Maestro CLI has, no fix
  // possible without privileged WebDriver access.
  _collectRoots: function() {
    var roots = [document];
    var skipped = 0;
    function walk(root) {
      var elements;
      try { elements = root.querySelectorAll('*'); } catch (e) { return; }
      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        var tag = el.tagName;
        if (tag === 'IFRAME' || tag === 'FRAME') {
          var inner = null;
          try { inner = el.contentDocument; } catch (e) { skipped++; continue; }
          if (inner === null) {
            // cross-origin OR sandboxed iframe (sandbox without
            // allow-same-origin) returns null
            skipped++;
            continue;
          }
          if (roots.indexOf(inner) === -1) {
            roots.push(inner);
            walk(inner);
          }
          continue;
        }
        // Open shadow root attached to this element — descend
        if (el.shadowRoot && roots.indexOf(el.shadowRoot) === -1) {
          roots.push(el.shadowRoot);
          walk(el.shadowRoot);
        }
      }
    }
    walk(document);
    this._lastCrossOriginSkips = skipped;
    return roots;
  },

  // Returns the number of cross-origin / sandboxed frames skipped during the
  // most recent _collectRoots pass. Used by the Go finders to enrich
  // not-found error messages.
  getLastCrossOriginSkips: function() {
    return this._lastCrossOriginSkips || 0;
  },

  findByText: function(text) {
    var lower = text.toLowerCase();
    var docs = this._collectRoots();
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
    var docs = this._collectRoots();
    for (var d = 0; d < docs.length; d++) {
      try {
        var el = docs[d].querySelector(selector);
        if (el) return el;
      } catch (e) {}
    }
    throw new Error('not found: ' + selector);
  },

  // Two-stage cross-root text-index lookup. Step 1 enumerates all matches
  // across same-origin frames + open shadow roots, stashes the requested
  // element in _lastIndexedMatch, and returns the total match count by
  // value. Step 2 retrieves the stashed element as a remote handle.
  //
  // The split is structural, not optional: rod.Eval(...).ByObject() can
  // stall when the result is a JS null (no remote objectId to track), so
  // we never request .ByObject() unless we know an element exists.
  // (Issue #72.)
  findByTextAt_count: function(text, index) {
    // Strict, semantic matching for `text + index`. An element matches only
    // when ALL of:
    //  * its own text (direct text-node children, no descendants), or its
    //    aria-label, or placeholder equals `text` (case-insensitive,
    //    trimmed). Avoids matching ancestors whose `textContent` transitively
    //    includes the match.
    //  * it's the kind of element a user could meaningfully act on — an
    //    <a>/<button>/<input>/<select>/<textarea>, or has role/tabindex/
    //    aria-label attached. Filters out decorative/structural elements
    //    like <code>, <p>, <h1> that would otherwise inflate the index.
    // This approximates Maestro CLI's AX-tree-named-element behaviour
    // without the cost of a per-frame Accessibility.queryAXTree round-trip.
    // Issue #72.
    var lower = text.toLowerCase();
    var INTERACTIVE_TAGS = { A:1, BUTTON:1, INPUT:1, SELECT:1, TEXTAREA:1 };
    function isInteractive(el) {
      if (INTERACTIVE_TAGS[el.tagName]) return true;
      if (!el.getAttribute) return false;
      if (el.getAttribute('role')) return true;
      if (el.getAttribute('tabindex') !== null) return true;
      if (el.getAttribute('aria-label')) return true;
      return false;
    }
    var roots = this._collectRoots();
    var matches = [];
    for (var r = 0; r < roots.length; r++) {
      var all;
      try { all = roots[r].querySelectorAll('*'); } catch (e) { continue; }
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        if (!isInteractive(el)) continue;
        var label = (el.getAttribute('aria-label') || '').toLowerCase();
        var ph = (el.getAttribute('placeholder') || '').toLowerCase();
        var ownText = '';
        for (var c = 0; c < el.childNodes.length; c++) {
          var n = el.childNodes[c];
          if (n.nodeType === 3) ownText += n.nodeValue;
        }
        ownText = ownText.trim().toLowerCase();
        if (ownText === lower || label === lower || ph === lower) {
          matches.push(el);
        }
      }
    }
    this._lastIndexedMatch = matches[index] || null;
    return matches.length;
  },

  findByTextAt_get: function() {
    return this._lastIndexedMatch;
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
    var docs = this._collectRoots();
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
      case 'id': {
        // Mirror the Go-side findByID cascade: #id → [data-testid] → [id*=]
        // → [name=] → [aria-label=]. First strategy that hits in any frame wins.
        var idCascade = [
          function(doc) {
            try { return doc.getElementById(selectorValue); } catch (e) { return null; }
          },
          function(doc) {
            try { return doc.querySelector('[data-testid="' + escaped + '"]'); } catch (e) { return null; }
          },
          function(doc) {
            try { return doc.querySelector('[id*="' + escaped + '"]'); } catch (e) { return null; }
          },
          function(doc) {
            try { return doc.querySelector('[name="' + escaped + '"]'); } catch (e) { return null; }
          },
          function(doc) {
            try { return doc.querySelector('[aria-label="' + escaped + '"]'); } catch (e) { return null; }
          },
        ];
        for (var s = 0; s < idCascade.length && results.length === 0; s++) {
          for (var d = 0; d < docs.length; d++) {
            var el = idCascade[s](docs[d]);
            if (el) { results.push(el); break; }
          }
        }
        break;
      }
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
