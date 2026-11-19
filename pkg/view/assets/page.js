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

  // The sections become pages, one at a time, and the stations of the line
  // pick what the overview shows below it. The address keeps the choice, so
  // a link and the back button land where the reader was.
  function pages() {
    var nav = document.querySelector(".pages");
    var all = [].slice.call(document.querySelectorAll("main > .page"));
    if (!nav || all.length < 2) return;
    var links = [].slice.call(nav.querySelectorAll("a"));
    var drill = document.getElementById("drill");
    var panes = drill ? [].slice.call(drill.querySelectorAll(".pane")) : [];
    var cards = [].slice.call(document.querySelectorAll(".line .st"));
    var whole = document.querySelector(".whole");
    var current = null;

    function page(id) {
      all.forEach(function (s) { s.hidden = s.id !== id; });
      links.forEach(function (a) {
        if (a.getAttribute("href") === "#" + id) a.setAttribute("aria-current", "page");
        else a.removeAttribute("aria-current");
      });
      current = id;
    }
    function station(id) {
      var found = false;
      panes.forEach(function (p) {
        var on = p.id === "station-" + id;
        p.hidden = !on;
        found = found || on;
      });
      if (!found) return false;
      cards.forEach(function (c) {
        var on = c.getAttribute("data-station") === id;
        c.classList.toggle("is-on", on);
        var a = c.querySelector("a");
        if (on) a.setAttribute("aria-current", "true");
        else a.removeAttribute("aria-current");
      });
      if (whole) whole.classList.toggle("on", id === "line");
      return true;
    }
    function first() {
      var pick = null;
      panes.forEach(function (p) { if (!pick && p.hasAttribute("data-constraint")) pick = p; });
      cards.forEach(function (c) {
        if (!pick && !c.classList.contains("is-off")) pick = document.getElementById("station-" + c.getAttribute("data-station"));
      });
      return (pick || panes[0] || { id: "" }).id.replace(/^station-/, "");
    }
    function route() {
      var id = location.hash.slice(1);
      if (id.indexOf("station-") === 0 && station(id.slice(8))) {
        if (current !== "overview") window.scrollTo(0, 0);
        page("overview");
        return;
      }
      var known = all.some(function (s) { return s.id === id; });
      var next = known ? id : "overview";
      if (next !== current) window.scrollTo(0, 0);
      page(next);
    }
    // A station is picked in place: the page does not move.
    function pick(e) {
      var a = e.target.closest("a[href^='#station-']");
      if (!a || !drill || e.button || e.metaKey || e.ctrlKey || e.shiftKey) return;
      var id = a.getAttribute("href").slice(9);
      if (!station(id)) return;
      e.preventDefault();
      if (current !== "overview") { page("overview"); window.scrollTo(0, 0); }
      if (history.pushState) history.pushState(null, "", "#station-" + id);
    }
    document.addEventListener("click", pick);
    station(first());
    route();
    window.addEventListener("hashchange", route);
    window.addEventListener("popstate", route);
  }

  // A note opens below its mark, and moves sideways to stay in the window.
  function notes() {
    function fit(e) {
      var box = e.target.closest && e.target.closest(".info");
      if (!box) return;
      var pop = box.querySelector(".pop");
      pop.style.transform = "";
      var r = pop.getBoundingClientRect();
      var shift = 0;
      if (r.left < 8) shift = 8 - r.left;
      else if (r.right > window.innerWidth - 8) shift = window.innerWidth - 8 - r.right;
      if (shift) pop.style.transform = "translateX(" + Math.round(shift) + "px)";
    }
    // A shift is cleared when the note closes, so a hidden note never holds
    // a place outside the window.
    function reset(e) {
      var box = e.target.closest && e.target.closest(".info");
      if (box) box.querySelector(".pop").style.transform = "";
    }
    document.addEventListener("pointerover", fit);
    document.addEventListener("focusin", fit);
    document.addEventListener("pointerout", reset);
    document.addEventListener("focusout", reset);
    // Escape closes a note opened from the keyboard.
    document.addEventListener("keydown", function (e) {
      var a = document.activeElement;
      if (e.key === "Escape" && a && a.classList.contains("info-b")) a.blur();
    });
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

  function ready() { theme(); pages(); notes(); tips(); menus(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", ready);
  else ready();
})();
