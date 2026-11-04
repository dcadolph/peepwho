// The guided tour: the whole product thesis as one two-minute walk.
//
// A visitor who lands on the demo sees capabilities and has to assemble the
// narrative alone: that this is one map answering three questions. The story
// assembles it for them: ask a question, see why the answer is trusted, see how
// the same person solved it before, and see what the organization loses if
// they leave. Every step reads the live index rather than a script, so the
// names on screen are whatever the data actually says.
(function () {
  "use strict";

  const EXAMPLE = "who do I talk to about billing retries";
  let step = -1;
  let top = null; // the live answer's first person, carried across steps

  // waitFor polls until the selector yields an element with size, so a step
  // never spotlights a target that has not rendered yet.
  function waitFor(selector, tries) {
    return new Promise((resolve) => {
      let left = tries || 40;
      const look = () => {
        const node = document.querySelector(selector);
        if (node && node.offsetParent !== null) return resolve(node);
        if (--left <= 0) return resolve(null);
        setTimeout(look, 100);
      };
      look();
    });
  }

  function api(path) {
    return fetch(path, { headers: { Accept: "application/json" } })
      .then((r) => (r.ok ? r.json() : null))
      .catch(() => null);
  }

  // The steps. Each prepares the page, names its spotlight, and says one thing.
  const STEPS = [
    {
      title: "Ask in plain words",
      why: "No syntax, no picking a tool. The answer is people, ranked, with a channel to ask in.",
      async prep() {
        location.hash = "#/";
        const input = document.getElementById("q");
        if (input && !input.value.trim()) {
          input.value = input.placeholder || EXAMPLE;
          if (typeof ask === "function") ask();
        }
        const card = await waitFor("#people .card");
        const data = await api("/api/ask?q=" + encodeURIComponent(EXAMPLE));
        top = data && data.people && data.people[0] ? data.people[0] : null;
        return card;
      },
    },
    {
      title: "The reasons are the ranking",
      why: "No model weighed in. Match strength is arithmetic over the work itself, so every answer can be checked.",
      async prep() {
        location.hash = "#/";
        return waitFor("#people .card .chips");
      },
    },
    {
      title: "How it was solved last time",
      why: "Recall finds the conversations where this was worked out, and points back to the source. A pointer, never a transcript.",
      async prep() {
        if (!top) return null;
        location.hash = "#/recall";
        const me = document.getElementById("recall-me");
        const q = document.getElementById("recall-q");
        if (me) me.value = top.id || top.email || "";
        if (q) q.value = "billing retries";
        if (typeof openRecall === "function") openRecall();
        return waitFor("#recall-list .card");
      },
    },
    {
      title: "What leaves if they leave",
      why: "The same map, read the other way: the subjects this person holds alone are what the organization loses.",
      async prep() {
        if (!top) return null;
        location.hash = "#/exposure";
        if (typeof checkDeparture === "function") checkDeparture(top.id || top.name);
        return waitFor("#exp-dep-result .exp-card");
      },
    },
    {
      title: "One map. Three questions.",
      why: "Who knows, how it was solved, and where you are thin. Local by default, nothing sent to anyone.",
      async prep() {
        return null; // closing card, no spotlight
      },
      closing: true,
    },
  ];

  // --- presentation -----------------------------------------------------

  let backdrop, ring, card;

  function build() {
    backdrop = document.createElement("div");
    backdrop.className = "story-backdrop";
    ring = document.createElement("div");
    ring.className = "story-ring";
    card = document.createElement("div");
    card.className = "story-card";
    card.setAttribute("role", "dialog");
    card.setAttribute("aria-label", "Guided story");
    backdrop.appendChild(ring);
    backdrop.appendChild(card);
    document.body.appendChild(backdrop);
    backdrop.addEventListener("click", (ev) => {
      if (ev.target === backdrop) stop();
    });
  }

  function spotlight(node) {
    if (!node) {
      ring.style.display = "none";
      return;
    }
    node.scrollIntoView({ block: "center", behavior: "auto" });
    const r = node.getBoundingClientRect();
    ring.style.display = "";
    ring.style.top = r.top - 8 + "px";
    ring.style.left = r.left - 8 + "px";
    ring.style.width = r.width + 16 + "px";
    ring.style.height = r.height + 16 + "px";
  }

  function caption(s, node) {
    const dots = STEPS.map((_, i) =>
      '<span class="story-dot' + (i === step ? " on" : "") + '"></span>').join("");
    card.innerHTML =
      '<div class="story-kicker">Guided tour &middot; 2 min</div>' +
      "<h2>" + s.title + "</h2>" +
      "<p>" + s.why + "</p>" +
      (s.closing
        ? '<pre class="story-install">go install github.com/dcadolph/peepwho@latest\npeepwho demo</pre>'
        : "") +
      '<div class="story-row"><div class="story-dots">' + dots + "</div>" +
      '<div class="story-btns">' +
      (step > 0 ? '<button class="story-b" data-act="back">Back</button>' : "") +
      '<button class="story-b story-next" data-act="next">' +
      (s.closing ? "Done" : "Next") + "</button>" +
      '<button class="story-b story-skip" data-act="skip">Skip</button>' +
      "</div></div>";
    card.classList.toggle("story-center", !node);
    card.querySelectorAll("[data-act]").forEach((b) => {
      b.addEventListener("click", () => {
        const act = b.getAttribute("data-act");
        if (act === "next") s.closing ? stop() : go(step + 1);
        else if (act === "back") go(step - 1);
        else stop();
      });
    });
    const next = card.querySelector(".story-next");
    if (next) next.focus();
  }

  async function go(n) {
    step = Math.max(0, Math.min(n, STEPS.length - 1));
    const s = STEPS[step];
    ring.style.display = "none";
    card.innerHTML = '<div class="story-kicker">Guided tour &middot; 2 min</div><p>…</p>';
    const node = await s.prep();
    spotlight(node);
    caption(s, node);
  }

  function stop() {
    if (backdrop) backdrop.remove();
    backdrop = null;
    document.removeEventListener("keydown", keys, true);
    step = -1;
  }

  function keys(ev) {
    if (!backdrop) return;
    if (ev.key === "Escape") { ev.stopPropagation(); stop(); }
    else if (ev.key === "ArrowRight") go(step + 1);
    else if (ev.key === "ArrowLeft") go(step - 1);
  }

  function start() {
    if (backdrop) return;
    build();
    document.addEventListener("keydown", keys, true);
    go(0);
  }

  // Entry points: the sidebar button, and ?story=1 for a link straight into it.
  const button = document.getElementById("story-btn");
  if (button) button.addEventListener("click", start);
  if (new URLSearchParams(location.search).get("story")) {
    // Let the landing auto-run settle first, so step one spotlights a result.
    setTimeout(start, 600);
  }
})();
