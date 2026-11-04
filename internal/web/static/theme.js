// peepwho theme engine. One shared file across every peepwho UI. Themes are pure
// CSS token sets selected by data-theme on the root; this only stores the choice
// and reflects it, so no per-page work is needed. Runs in <head> before paint to
// avoid a flash of the wrong theme (the page CSP allows same-origin scripts).
(function () {
	"use strict";
	var KEY = "peepwho-theme";
	var THEMES = ["radar", "ink", "loom"];
	var DEFAULT = "radar";

	function current() {
		try {
			var t = localStorage.getItem(KEY);
			return THEMES.indexOf(t) >= 0 ? t : DEFAULT;
		} catch (e) { return DEFAULT; }
	}
	function mark(t) {
		var btns = document.querySelectorAll("[data-theme-btn]");
		for (var i = 0; i < btns.length; i++) {
			btns[i].classList.toggle("on", btns[i].getAttribute("data-theme-btn") === t);
			btns[i].setAttribute("aria-pressed", btns[i].getAttribute("data-theme-btn") === t ? "true" : "false");
		}
	}
	function apply(t) {
		if (THEMES.indexOf(t) < 0) t = DEFAULT;
		document.documentElement.setAttribute("data-theme", t);
		try { localStorage.setItem(KEY, t); } catch (e) { /* private mode */ }
		mark(t);
	}

	// Set the attribute immediately so the first paint uses the saved theme.
	document.documentElement.setAttribute("data-theme", current());

	function wire() {
		var btns = document.querySelectorAll("[data-theme-btn]");
		for (var i = 0; i < btns.length; i++) {
			(function (b) { b.addEventListener("click", function () { apply(b.getAttribute("data-theme-btn")); }); })(btns[i]);
		}
		mark(current());
	}
	if (document.readyState !== "loading") wire();
	else document.addEventListener("DOMContentLoaded", wire);

	window.peepwhoTheme = { apply: apply, current: current, themes: THEMES };
})();
