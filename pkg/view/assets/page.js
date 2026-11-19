(function () {
  "use strict";
  var root = document.documentElement;
  var KEY = "factorymeter.theme";
  root.classList.add("js");

  // The theme is set before the page paints. The choice lives in this
  // browser only, and the page works when storage is closed to it.
  try {
    var kept = localStorage.getItem(KEY);
    if (kept === "light" || kept === "dark") root.setAttribute("data-theme", kept);
  } catch (e) {}

  function dark() {
    var t = root.getAttribute("data-theme");
    if (t) return t === "dark";
    return !!window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  function el(name, cls) {
    var e = document.createElement(name);
    if (cls) e.className = cls;
    return e;
  }

  function theme() {
    var btn = document.getElementById("theme");
    if (!btn) return;
    function paint() {
      btn.textContent = dark() ? "Light" : "Dark";
      btn.setAttribute("aria-label", dark() ? "Switch to the light theme" : "Switch to the dark theme");
    }
    btn.hidden = false;
    btn.addEventListener("click", function () {
      var next = dark() ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem(KEY, next); } catch (e) {}
      paint();
    });
    paint();
  }

  // The stations become tabs: one at a time, the constraint first.
  function tabs() {
    var host = document.getElementById("stations");
    if (!host) return;
    var panes = [].slice.call(host.querySelectorAll(".pane"));
    if (panes.length < 2) return;
    var list = el("div", "tabs");
    list.setAttribute("role", "tablist");
    list.setAttribute("aria-label", "Stations");
    var buttons = panes.map(function (p, i) {
      var b = el("button");
      b.type = "button";
      b.id = "tab-" + p.id;
      b.setAttribute("role", "tab");
      b.setAttribute("aria-controls", p.id);
      b.textContent = p.getAttribute("data-name");
      if (p.hasAttribute("data-constraint")) {
        var badge = el("span", "badge");
        badge.textContent = "Constraint";
        b.appendChild(badge);
      }
      p.setAttribute("role", "tabpanel");
      p.setAttribute("aria-labelledby", b.id);
      b.addEventListener("click", function () { show(i, true); });
      b.addEventListener("keydown", function (e) {
        var d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
        if (!d) return;
        e.preventDefault();
        var n = (i + d + panes.length) % panes.length;
        show(n, true);
        buttons[n].focus();
      });
      list.appendChild(b);
      return b;
    });
    host.insertBefore(list, panes[0]);

    function show(n, keep) {
      panes.forEach(function (p, i) {
        p.hidden = i !== n;
        buttons[i].setAttribute("aria-selected", i === n ? "true" : "false");
        buttons[i].tabIndex = i === n ? 0 : -1;
      });
      if (keep && history.replaceState) history.replaceState(null, "", "#" + panes[n].id);
    }
    function fromHash(scroll) {
      var id = location.hash.slice(1);
      for (var i = 0; i < panes.length; i++) {
        if (panes[i].id === id) {
          show(i, false);
          if (scroll) host.scrollIntoView();
          return true;
        }
      }
      return false;
    }
    var start = 0;
    panes.forEach(function (p, i) { if (p.hasAttribute("data-constraint")) start = i; });
    show(start, false);
    fromHash(true);
    window.addEventListener("hashchange", function () { fromHash(true); });
  }

  // One tooltip for every chart. It names the week under the pointer and
  // every series in it. The week is found from the geometry of the chart.
  function tips() {
    var charts = document.querySelectorAll("svg.chart");
    if (!charts.length) return;
    var weeks = (document.body.getAttribute("data-weeks") || "").split("|");
    var tip = el("div", "tip");
    tip.hidden = true;
    tip.setAttribute("aria-hidden", "true");
    document.body.appendChild(tip);
    var cross = null;

    function hide() {
      tip.hidden = true;
      if (cross) cross.setAttribute("visibility", "hidden");
    }
    function num(svg, name) { return parseFloat(svg.getAttribute(name)) || 0; }
    function fill(svg, i) {
      var names = (svg.getAttribute("data-names") || "").split("|");
      var keys = (svg.getAttribute("data-keys") || "").split("|");
      var rows = (svg.getAttribute("data-v") || "").split("~");
      while (tip.firstChild) tip.removeChild(tip.firstChild);
      var head = el("div", "tip-h");
      head.textContent = weeks[i] || "";
      tip.appendChild(head);
      rows.forEach(function (r, k) {
        var part = (r.split("|")[i] || "").split(";");
        var row = el("div", "tip-r");
        if (names[k]) row.appendChild(el("span", "k " + (keys[k] || "s1")));
        var value = el("strong");
        value.textContent = part[0] || "no data";
        row.appendChild(value);
        if (names[k]) {
          var name = el("span", "tip-n");
          name.textContent = names[k];
          row.appendChild(name);
        }
        if (part[1]) {
          var items = el("span", "tip-m");
          items.textContent = part[1] === "1" ? "1 item" : part[1] + " items";
          row.appendChild(items);
        }
        tip.appendChild(row);
      });
    }
    function place(e) {
      var gap = 14, w = tip.offsetWidth, h = tip.offsetHeight;
      var x = e.clientX + gap, y = e.clientY + gap;
      if (x + w > window.innerWidth - 8) x = e.clientX - gap - w;
      if (y + h > window.innerHeight - 8) y = e.clientY - gap - h;
      tip.style.left = Math.max(8, x) + "px";
      tip.style.top = Math.max(8, y) + "px";
    }
    function over(e) {
      var svg = e.currentTarget;
      var box = svg.getBoundingClientRect();
      var view = svg.viewBox.baseVal;
      if (!box.width || !view.width) return;
      var step = num(svg, "data-step");
      var count = (svg.getAttribute("data-v") || "").split("~")[0].split("|").length;
      var x = (e.clientX - box.left) * view.width / box.width;
      var i = Math.floor((x - num(svg, "data-x")) / step);
      if (!step || i < 0 || i >= count) { hide(); return; }
      if (!cross || cross.parentNode !== svg) {
        if (cross && cross.parentNode) cross.parentNode.removeChild(cross);
        cross = document.createElementNS("http://www.w3.org/2000/svg", "rect");
        cross.setAttribute("class", "cross");
        svg.appendChild(cross);
      }
      cross.setAttribute("x", num(svg, "data-x") + i * step);
      cross.setAttribute("y", num(svg, "data-y"));
      cross.setAttribute("width", step);
      cross.setAttribute("height", num(svg, "data-h"));
      cross.setAttribute("visibility", "visible");
      fill(svg, i);
      tip.hidden = false;
      place(e);
    }
    [].forEach.call(charts, function (svg) {
      svg.addEventListener("pointermove", over);
      svg.addEventListener("pointerdown", over);
      svg.addEventListener("pointerleave", hide);
    });
    window.addEventListener("scroll", hide, { passive: true });
  }

  // A menu closes when the reader clicks away from it.
  function menus() {
    document.addEventListener("click", function (e) {
      [].forEach.call(document.querySelectorAll("details.menu[open]"), function (d) {
        if (!d.contains(e.target)) d.removeAttribute("open");
      });
    });
  }

  function ready() { theme(); tabs(); tips(); menus(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", ready);
  else ready();
})();
