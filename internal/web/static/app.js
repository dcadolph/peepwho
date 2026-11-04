// peepwho web UI: an ask view over /api/ask and directory views over
// /api/directory, routed by location.hash. Result text is set with
// textContent so indexed data cannot inject markup.

const form = document.getElementById("ask-form");
const qInput = document.getElementById("q");
const modeSeg = document.getElementById("mode-seg");
const statusEl = document.getElementById("status");
const summaryEl = document.getElementById("summary");
const peopleSection = document.getElementById("people-section");
const channelsSection = document.getElementById("channels-section");
const peopleEl = document.getElementById("people");
const channelsEl = document.getElementById("channels");
const button = form.querySelector("button");
const examplesEl = document.getElementById("examples");
const askView = document.getElementById("view-ask");
const dirView = document.getElementById("view-directory");
const recallView = document.getElementById("view-recall");
const recallForm = document.getElementById("recall-form");
const recallQuery = document.getElementById("recall-q");
const recallMe = document.getElementById("recall-me");
const recallStatus = document.getElementById("recall-status");
const recallList = document.getElementById("recall-list");
const recallScope = document.getElementById("recall-scope");
const recallSince = document.getElementById("recall-since");
const recallSources = document.getElementById("recall-sources");
const recallSlabel = document.getElementById("recall-slabel");
const recallCount = document.getElementById("recall-count");
const expView = document.getElementById("view-exposure");
const expStatus = document.getElementById("exp-status");
const expRisk = document.getElementById("exp-risk");
const expDrift = document.getElementById("exp-drift");
const expRegions = document.getElementById("exp-regions");
const expSpans = document.getElementById("exp-spans");
const expDepInput = document.getElementById("exp-dep-input");
const expDepGo = document.getElementById("exp-dep-go");
const expDepResult = document.getElementById("exp-dep-result");
const expDepPeople = document.getElementById("exp-dep-people");
const expProofGo = document.getElementById("exp-proof-go");
const expProofOut = document.getElementById("exp-proof-out");
const cliView = document.getElementById("view-cli");
const cliTabs = document.getElementById("cli-tabs");
const cliOut = document.getElementById("cli-out");
const cliStatus = document.getElementById("cli-status");
const cliNote = document.getElementById("cli-note");
// The commands the demo can print, in the order somebody would run them.
const CLI_COMMANDS = [
  { cmd: "ask", label: "peepwho ask" },
  { cmd: "ask-llm", label: "peepwho ask --mode llm", recorded: true },
  { cmd: "risk", label: "peepwho risk" },
  { cmd: "ownership", label: "peepwho ownership" },
  { cmd: "related", label: "peepwho related" },
  { cmd: "attest", label: "peepwho attest" },
];
let cliCurrent = "";
let recallData = null;
let recallSourceFilter = null;
let recallTimer = null;
const dirTitle = document.getElementById("dir-title");
const dirFilter = document.getElementById("dir-filter");
const dirStatus = document.getElementById("dir-status");
const dirList = document.getElementById("dir-list");
const sideNav = document.getElementById("side-nav");
const dirSort = document.getElementById("dir-sort");
const dirControls = document.querySelector(".dir-controls");
const facetTeam = document.getElementById("facet-team");
const facetOrg = document.getElementById("facet-org");

// The active answer mode and AI provider, driven by the segmented controls.
let currentMode = "keyword";
let currentProvider = "ollama";

// providerTouched blocks the server default from stomping a user's pick.
let providerTouched = false;

// modesReport holds readiness from /api/modes: modes, providers, provider.
let modesReport = { modes: {}, providers: {} };

const providerSeg = document.getElementById("provider-seg");

// PROVIDER_LABELS names providers the way people know them.
const PROVIDER_LABELS = {
  ollama: "the local model", anthropic: "Claude", openai: "ChatGPT", gemini: "Gemini",
};

modeSeg.addEventListener("click", (event) => {
  const btn = event.target.closest(".seg-btn");
  if (!btn) return;
  currentMode = btn.dataset.mode;
  for (const b of modeSeg.querySelectorAll(".seg-btn")) {
    b.classList.toggle("active", b === btn);
    b.setAttribute("aria-pressed", String(b === btn));
  }
  providerSeg.hidden = currentMode !== "llm";
  showModeHint();
  if (currentMode !== "keyword") loadModes().then(showModeHint);
});

providerSeg.addEventListener("click", (event) => {
  const btn = event.target.closest(".seg-btn");
  if (!btn) return;
  currentProvider = btn.dataset.provider;
  providerTouched = true;
  syncProviderButtons();
  showModeHint();
  loadModes().then(showModeHint);
});

// syncProviderButtons marks the active provider button.
function syncProviderButtons() {
  for (const b of providerSeg.querySelectorAll(".seg-btn")) {
    const active = b.dataset.provider === currentProvider;
    b.classList.toggle("active", active);
    b.setAttribute("aria-pressed", String(active));
  }
}

// providerInfo is the readiness of the currently selected AI provider.
function providerInfo() {
  return (modesReport.providers || {})[currentProvider] || (modesReport.modes || {}).llm;
}

// loadModes refreshes readiness, marks unready modes and providers with a
// dot, and upgrades tooltips with the specific guidance.
async function loadModes() {
  try {
    const res = await fetch("/api/modes");
    if (!res.ok) return;
    modesReport = await res.json();
  } catch (err) {
    return;
  }
  if (!providerTouched && modesReport.provider) {
    currentProvider = modesReport.provider;
    syncProviderButtons();
  }
  const modes = modesReport.modes || {};
  for (const b of modeSeg.querySelectorAll(".seg-btn")) {
    const info = b.dataset.mode === "llm" ? providerInfo() : modes[b.dataset.mode];
    if (!info) continue;
    b.classList.toggle("warn", info.ready === false);
    // The AI button keeps its generic tooltip; provider buttons carry the
    // provider-specific guidance.
    if (info.hint && b.dataset.mode !== "llm") b.dataset.tip = info.hint;
  }
  const providers = modesReport.providers || {};
  for (const b of providerSeg.querySelectorAll(".seg-btn")) {
    const info = providers[b.dataset.provider];
    if (!info) continue;
    b.classList.toggle("warn", info.ready === false);
    if (info.hint) b.dataset.tip = info.hint;
  }
}

// showModeHint puts the selected mode or provider's guidance on the status
// line, so picking AI without a model tells you what to do before you ask.
function showModeHint() {
  const info = currentMode === "llm" ? providerInfo() : (modesReport.modes || {})[currentMode];
  if (info && info.hint) statusEl.textContent = info.hint;
}

// showExamples offers a few questions this index can actually answer, taken
// from the subjects the most people work on. Hardcoded examples go stale the
// moment they name something the index does not have, and a suggested question
// that returns nothing is the worst possible first impression.
async function showExamples() {
  let subjects = [];
  try {
    const dir = await directory();
    subjects = (dir.topics || []).slice(0, 3).map((t) => String(t.name).replace(/-/g, " "));
  } catch (err) {
    // Fall through to the generic prompts below.
  }
  const queries = subjects.length
    ? subjects.map((s, i) => (i === 0 ? "who do I talk to about " + s : "who knows " + s))
    : ["who do I talk to about billing", "who knows kubernetes", "vacation policy"];
  examplesEl.replaceChildren();
  for (const example of queries) {
    const b = el("button", "example", example);
    b.type = "button";
    b.addEventListener("click", () => runAsk(example));
    examplesEl.appendChild(b);
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  await ask();
});

async function ask() {
  const q = qInput.value.trim();
  if (!q) return;

  button.disabled = true;
  examplesEl.hidden = true;
  clearResults();
  statusEl.textContent =
    currentMode === "llm" ? "Asking " + (PROVIDER_LABELS[currentProvider] || "the model") + "..."
    : currentMode === "semantic" ? "Searching by meaning..."
    : "Searching...";

  try {
    const params = new URLSearchParams({ q, mode: currentMode });
    if (currentMode === "llm" && currentProvider) params.set("provider", currentProvider);
    setParam("person", "");
    setParam("q", q);
    const res = await fetch("/api/ask?" + params.toString());
    const data = await res.json();
    if (!res.ok) {
      statusEl.textContent = "Error: " + (data.error || res.statusText);
      return;
    }
    render(data);
  } catch (err) {
    statusEl.textContent = "Request failed: " + err.message;
  } finally {
    button.disabled = false;
  }
}

// runAsk switches to the ask view and runs the query.
function runAsk(q) {
  qInput.value = q;
  if (location.hash && location.hash !== "#/") location.hash = "#/";
  showView("ask");
  ask();
}

// setParam updates one query parameter in the address bar without reloading.
function setParam(key, val) {
  const p = new URLSearchParams(location.search);
  if (val) {
    p.set(key, val);
  } else {
    p.delete(key);
  }
  const s = p.toString();
  history.replaceState(null, "", (s ? "?" + s : location.pathname) + location.hash);
}

function clearResults() {
  summaryEl.hidden = true;
  summaryEl.textContent = "";
  peopleEl.replaceChildren();
  channelsEl.replaceChildren();
  peopleSection.hidden = true;
  channelsSection.hidden = true;
}

function render(data) {
  if (data.summary) {
    summaryEl.textContent = data.summary;
    summaryEl.hidden = false;
  }
  const people = data.people || [];
  const channels = data.channels || [];

  people.forEach((p, i) => peopleEl.appendChild(personCard(p, data.query, i)));
  channels.forEach((c, i) => channelsEl.appendChild(channelCard(c, data.query, i)));
  peopleSection.hidden = people.length === 0;
  channelsSection.hidden = channels.length === 0;

  if (people.length === 0 && channels.length === 0) {
    statusEl.textContent =
      "No matches. Try fewer or different words, or browse Topics for what the index knows.";
  } else {
    statusEl.textContent = people.length + " people, " + channels.length + " channels";
  }
}

function chips(parent, items) {
  if (!items || !items.length) return;
  const wrap = el("div", "chips");
  for (const item of items) wrap.appendChild(el("span", "chip", item));
  parent.appendChild(wrap);
}

function strengthBadge(c) {
  if (!c) return null;
  const label = c >= 0.75 ? "strong" : c >= 0.45 ? "moderate" : "weak";
  return el("span", "conf conf-" + label, label);
}

function voteButtons(query, target) {
  const wrap = el("div", "votes");
  const note = document.createElement("input");
  note.className = "vote-note";
  note.type = "text";
  note.placeholder = "why? (optional)";
  for (const [label, vote] of [["helpful", "helpful"], ["wrong", "not-helpful"]]) {
    const button = el("button", "vote", label);
    button.type = "button";
    button.addEventListener("click", async () => {
      try {
        const res = await fetch("/api/feedback", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ query, vote, comment: note.value.trim(), ...target }),
        });
        wrap.replaceChildren(el("span", "voted", res.ok ? "thanks" : "failed"));
      } catch (err) {
        wrap.replaceChildren(el("span", "voted", "failed"));
      }
    });
    wrap.appendChild(button);
  }
  wrap.appendChild(note);
  return wrap;
}

// rankBadge renders the zero-padded position marker in a result card corner.
function rankBadge(i) {
  return el("span", "rank", String(i + 1).padStart(2, "0"));
}

// initials reduces a name to one or two letters for an avatar.
function initials(name) {
  const parts = (name || "").trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return "?";
  return (parts[0][0] + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase();
}

// avatarHue derives a stable hue from a name, so the same person is always the
// same color without storing anything.
function avatarHue(name) {
  let h = 0;
  for (let i = 0; i < (name || "").length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return h;
}

// personAvatar builds the round initials mark. It carries no information the
// row does not already state; it exists so a list of people scans as people.
function personAvatar(name) {
  const av = el("div", "avatar", initials(name));
  const hue = avatarHue(name || "");
  av.style.background = "hsl(" + hue + " 42% 22%)";
  av.style.color = "hsl(" + hue + " 70% 78%)";
  return av;
}

function personCard(p, query, i) {
  const card = el("div", "card person-card");
  card.appendChild(rankBadge(i));

  const head = el("div", "person-head");
  head.appendChild(personAvatar(p.name || p.email || ""));
  const who = el("div", "person-who");
  const name = el("div", "name");
  const toggle = el("button", "name-toggle", p.name || p.email || "unknown");
  toggle.type = "button";
  toggle.title = "Show everything peepwho knows";
  toggle.addEventListener("click", () => openProfile(p.id || p.email));
  name.appendChild(toggle);
  const badge = strengthBadge(p.strength);
  if (badge) name.appendChild(badge);
  who.appendChild(name);
  const sub = [p.title, p.team].filter(Boolean).join(" · ");
  if (sub) who.appendChild(el("div", "sub", sub));
  head.appendChild(who);

  // Contact sits at the end of the head row: the email and its copy button
  // together, so the row answers "who" on the left and "reach them" on the
  // right instead of stacking three lines of the same person.
  if (p.email) {
    const contact = el("div", "person-contact");
    contact.appendChild(el("span", "person-mail", p.email));
    const copyText = ((p.name || "") + " <" + p.email + ">").trim();
    contact.appendChild(copyButton(copyText));
    head.appendChild(contact);
  }
  card.appendChild(head);
  chips(card, p.reasons);
  if (query && p.id && document.body.dataset.voting) card.appendChild(voteButtons(query, { person: p.id }));
  return card;
}

async function openProfile(id) {
  if (!id) return;
  try {
    const res = await fetch("/api/person?id=" + encodeURIComponent(id));
    if (!res.ok) return;
    showProfile(await res.json());
  } catch (err) {
    // A failed lookup just leaves the page as it is.
  }
}

// A dialog has to say it is one, keep the keyboard inside itself, and hand
// focus back where it came from. Without that, opening a profile leaves the
// caret behind the modal: Tab walks the page underneath, and nothing announces
// that anything opened at all.
let modalReturn = null;

// openModal marks a backdrop as a dialog, moves focus into it, and remembers
// what to give focus back to.
function openModal(backdrop, label) {
  backdrop.setAttribute("role", "dialog");
  backdrop.setAttribute("aria-modal", "true");
  if (label) backdrop.setAttribute("aria-label", label);
  modalReturn = document.activeElement;
  document.body.appendChild(backdrop);
  const first = backdrop.querySelector(
    "button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])");
  (first || backdrop).focus();
  if (!first) backdrop.setAttribute("tabindex", "-1");
}

// closeModal removes a dialog and returns focus to whatever opened it, so the
// keyboard does not start again from the top of the page.
function closeModal(backdrop) {
  if (!backdrop) return;
  backdrop.remove();
  if (modalReturn && document.contains(modalReturn)) modalReturn.focus();
  modalReturn = null;
}

// trapTab keeps Tab inside the open dialog, cycling at either end.
function trapTab(event) {
  const backdrop = document.querySelector(".modal-backdrop[role='dialog']");
  if (!backdrop || event.key !== "Tab") return;
  const stops = [...backdrop.querySelectorAll(
    "button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])")]
    .filter((n) => n.offsetParent !== null);
  if (!stops.length) {
    event.preventDefault();
    return;
  }
  const first = stops[0], last = stops[stops.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

// personLink renders somebody's name as a way to reach them.
//
// A name is the most useful thing on the screen and was the most often dead: a
// channel listed its active members as plain text, and every knowledge-risk
// finding named the person it rests on without offering any way to find out who
// they are. It falls back to plain text when the finding carries a name but no
// identifier, since a button that cannot open anything is worse than a label.
function personLink(name, id) {
  const label = name || id || "";
  if (!id) return document.createTextNode(label);
  const button = el("button", "person-link", label);
  button.type = "button";
  button.title = "Show everything peepwho knows about " + label;
  button.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openProfile(id);
  });
  return button;
}

function safeHref(url) {
  var s = String(url == null ? "" : url).trim();
  return /^(https?:|mailto:)/i.test(s) ? s : "#";
}
function showProfile(p) {
  closeProfile();
  const backdrop = el("div", "modal-backdrop");
  backdrop.id = "profile-modal";
  backdrop.addEventListener("click", (event) => {
    if (event.target === backdrop) closeProfile();
  });
  const modal = el("div", "modal");

  const close = el("button", "modal-close", "close");
  close.type = "button";
  close.addEventListener("click", closeProfile);

  const header = el("div", "modal-head");
  header.appendChild(personAvatar(p.name || p.id || ""));
  const who = el("div", "person-who");
  who.appendChild(el("div", "name", p.name || p.id));
  const sub = [p.title, p.team, p.org].filter(Boolean).join(" · ");
  if (sub) who.appendChild(el("div", "sub", sub));
  header.appendChild(who);
  header.appendChild(close);
  modal.appendChild(header);

  const rows = el("div", "details");
  const row = (label, value) => {
    const r = el("div", "detail-row");
    r.appendChild(el("span", "detail-label", label));
    r.appendChild(value);
    rows.appendChild(r);
  };
  if (p.email) {
    const v = el("span", "detail-value", p.email);
    v.appendChild(copyButton(p.email));
    const mail = el("a", "detail-action", "email");
    mail.href = "mailto:" + p.email;
    v.appendChild(mail);
    row("Email", v);
  }
  if (p.id && p.id !== p.email) row("Id", el("span", "detail-value", p.id));
  if (p.identities && p.identities.length) {
    const v = el("span", "detail-value detail-chips");
    for (const raw of p.identities) {
      const cut = String(raw).indexOf(":");
      const chip = el("span", "chip chip-id");
      if (cut > 0) {
        chip.appendChild(el("span", "chip-src", String(raw).slice(0, cut)));
        chip.appendChild(document.createTextNode(String(raw).slice(cut + 1)));
      } else {
        chip.textContent = String(raw);
      }
      v.appendChild(chip);
    }
    row("Also known as", v);
  }
  if (p.joins && p.joins.length) {
    const v = el("span", "detail-value detail-joins");
    for (const j of p.joins) {
      const line = el("div", "join-line");
      line.appendChild(el("span", "join-alias", j.alias));
      const pct = Math.round((j.confidence || 0) * 100);
      const bar = el("span", "join-bar");
      const fill = el("span", "join-bar-fill");
      fill.style.width = pct + "%";
      bar.appendChild(fill);
      line.appendChild(bar);
      line.appendChild(el("span", "join-pct", pct + "%"));
      line.appendChild(el("span", "join-reason", j.reason || ""));
      v.appendChild(line);
    }
    row("Merged by", v);
  }
  if (p.manager && (p.manager.name || p.manager.email)) {
    row("Manager", el("span", "detail-value", p.manager.name || p.manager.email));
  }
  if (p.channels && p.channels.length) {
    const v = el("span", "detail-value detail-chips");
    for (const c of p.channels) v.appendChild(el("span", "chip chip-channel", "#" + c));
    row("Active in", v);
  }
  if (p.topics && p.topics.length) {
    const v = el("span", "detail-value detail-chips");
    for (const topic of p.topics) v.appendChild(el("span", "chip", topic));
    row("Knows about", v);
  }
  modal.appendChild(rows);
  backdrop.appendChild(modal);
  openModal(backdrop, "Profile: " + (p.name || p.email || "person"));
  setParam("person", p.id);
}

function closeProfile() {
  const open = document.getElementById("profile-modal");
  if (open) {
    closeModal(open);
    setParam("person", "");
  }
}

// The feedback bundle is offered, never sent: this panel only explains the
// command that writes the redacted file, because a web page with nothing to
// send is the strongest possible privacy claim about what it sends.
function showFeedbackHow() {
  const backdrop = el("div", "modal-backdrop");
  backdrop.id = "fbhow-modal";
  backdrop.addEventListener("click", (ev) => {
    if (ev.target === backdrop) closeModal(backdrop);
  });
  const box = el("div", "modal keys-modal");
  box.appendChild(el("h2", null, "Tell us how it went"));
  const p1 = el("p", "fb-how-p",
    "Your votes on answers stay on this machine and tune your own ranking. " +
    "If you want to send us what they amount to, one command writes a file:");
  const pre = el("pre", "story-install", "peepwho feedback bundle");
  pre.appendChild(copyButton("peepwho feedback bundle"));
  const p2 = el("p", "fb-how-p",
    "It holds vote counts, the word counts of the questions, and the comments " +
    "you typed. Queries, names, and messages never appear in it. Read the " +
    "file, then attach it to a GitHub issue or an email yourself. Nothing here " +
    "can send it, on purpose.");
  box.appendChild(p1);
  box.appendChild(pre);
  box.appendChild(p2);
  backdrop.appendChild(box);
  openModal(backdrop, "How to send feedback");
}
const fbHow = document.getElementById("fb-how");
if (fbHow) fbHow.addEventListener("click", showFeedbackHow);

// typing reports whether the caret is somewhere a keystroke means a character
// rather than a command, so a shortcut never eats a letter mid-question.
function typing() {
  const node = document.activeElement;
  if (!node) return false;
  const tag = node.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || node.isContentEditable;
}

// showShortcuts toggles the list of keys. Shortcuts nobody can discover are
// shortcuts nobody uses, so the page says what it answers to.
function showShortcuts() {
  const open = document.getElementById("keys-modal");
  if (open) {
    closeModal(open);
    return;
  }
  const backdrop = el("div", "modal-backdrop");
  backdrop.id = "keys-modal";
  backdrop.addEventListener("click", (ev) => {
    if (ev.target === backdrop) closeModal(backdrop);
  });
  const box = el("div", "modal keys-modal");
  box.appendChild(el("h2", null, "Keyboard"));
  const list = el("dl", "keys-list");
  const nav = [...sideNav.querySelectorAll("a")];
  for (const [key, what] of [
    ["/", "Search, or focus the question box"],
    ["1 - " + Math.min(nav.length, 9), "Jump to a section in the sidebar"],
    ["?", "Show or hide this list"],
    ["Esc", "Close whatever is open"],
  ]) {
    list.appendChild(el("dt", null, key));
    list.appendChild(el("dd", null, what));
  }
  box.appendChild(list);
  backdrop.appendChild(box);
  openModal(backdrop, "Keyboard shortcuts");
}

document.addEventListener("keydown", trapTab);

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    const keys = document.getElementById("keys-modal");
    if (keys) {
      closeModal(keys);
      return;
    }
    closeProfile();
    return;
  }
  if (typing() || event.metaKey || event.ctrlKey || event.altKey) return;
  if (event.key === "/") {
    event.preventDefault();
    (currentView === "ask" ? qInput : dirFilter).focus();
    return;
  }
  if (event.key === "?") {
    event.preventDefault();
    showShortcuts();
    return;
  }
  // A digit goes to that entry in the sidebar, counted as it is displayed, so
  // the number beside a section in the shortcuts list is the one that works.
  if (event.key >= "1" && event.key <= "9") {
    const nav = [...sideNav.querySelectorAll("a")];
    const target = nav[Number(event.key) - 1];
    if (target) {
      event.preventDefault();
      target.click();
      if (target.getAttribute("href").startsWith("#")) location.hash = target.getAttribute("href");
    }
  }
});

function channelCard(c, query, i) {
  const card = el("div", "card");
  card.appendChild(rankBadge(i));
  const name = el("div", "name", "#" + c.name);
  name.appendChild(copyButton("#" + c.name));
  const badge = strengthBadge(c.strength);
  if (badge) name.appendChild(badge);
  card.appendChild(name);
  if (c.topic) card.appendChild(el("div", "sub", c.topic));

  const members = c.members || [];
  if (members.length) {
    const sub = el("div", "sub");
    sub.appendChild(document.createTextNode("Active: "));
    members.forEach((m, i) => {
      if (i) sub.appendChild(document.createTextNode(", "));
      sub.appendChild(personLink(m.name || m.email || "", m.email || m.id));
    });
    card.appendChild(sub);
  }
  if (c.url) {
    const link = el("a", "card-open", "Open in Slack");
    link.href = safeHref(c.url);
    link.target = "_blank";
    link.rel = "noopener";
    card.appendChild(link);
  }
  chips(card, c.reasons);
  if (query && c.name && document.body.dataset.voting) card.appendChild(voteButtons(query, { channel: c.name }));
  return card;
}

// Directory views: one fetch, cached for the page's life, filtered client side.

const DIR_VIEWS = {
  people: {
    title: "People",
    empty: "No people indexed yet. Any source with authors fills this: try peepwho index --source git --repo-path <a repository>.",
  },
  channels: {
    title: "Channels",
    empty: "No channels indexed yet. Channels come from Slack, so this stays empty until peepwho index --source slack has run.",
  },
  teams: {
    title: "Teams",
    empty: "No teams indexed yet. Teams come from a source of record such as an org CSV or a directory, not from activity.",
  },
  topics: {
    title: "Topics",
    empty: "No topics indexed yet. Subjects are read out of the work itself, so indexing any source will fill this.",
  },
};

let dirPromise = null;

// directory fetches /api/directory once and caches the result.
function directory() {
  if (!dirPromise) {
    dirPromise = fetch("/api/directory").then((res) => {
      if (!res.ok) throw new Error(res.statusText);
      return res.json();
    });
    dirPromise.catch(() => (dirPromise = null));
  }
  return dirPromise;
}

let currentView = "ask";

// pendingTeamFacet carries a team chosen in the Teams view into the People
// facet on the next render.
let pendingTeamFacet = "";

// viewFromHash maps the location hash to a view name.
function viewFromHash() {
  const name = location.hash.replace(/^#\//, "");
  if (name === "recall" && recallView) return "recall";
  if (name === "exposure" && expView) return "exposure";
  if (name === "cli" && cliView) return "cli";
  return DIR_VIEWS[name] ? name : "ask";
}

// showView flips between the ask view and a directory view and marks the nav.
function showView(view) {
  currentView = view;
  askView.hidden = view !== "ask";
  dirView.hidden = view === "ask" || view === "recall" || view === "exposure" || view === "cli";
  if (recallView) recallView.hidden = view !== "recall";
  if (expView) expView.hidden = view !== "exposure";
  if (cliView) cliView.hidden = view !== "cli";
  for (const a of sideNav.querySelectorAll("a")) {
    a.classList.toggle("active", a.dataset.view === view);
  }
  facetTeam.hidden = view !== "people";
  facetOrg.hidden = view !== "people";
  if (view === "recall") {
    openRecall();
    return;
  }
  if (view === "exposure") {
    renderExposure();
    return;
  }
  if (view === "cli") {
    const want = new URLSearchParams(location.search).get("cmd");
    renderCLI(want || cliCurrent || CLI_COMMANDS[0].cmd);
    return;
  }
  if (view !== "ask") {
    dirTitle.textContent = DIR_VIEWS[view].title;
    dirFilter.placeholder = "Filter " + view + "...";
    renderDirectory(view);
  }
}

// fillFacets populates the team and org dropdowns from the people directory,
// once, and applies any team picked from the Teams view.
function fillFacets(people) {
  if (!facetTeam.options.length) {
    fillFacet(facetTeam, "All teams", people.map((p) => p.team));
    fillFacet(facetOrg, "All orgs", people.map((p) => p.org));
  }
  if (pendingTeamFacet) {
    facetTeam.value = pendingTeamFacet;
    if (facetTeam.value !== pendingTeamFacet) facetTeam.value = "";
    pendingTeamFacet = "";
  }
}

// fillFacet fills one dropdown with an all option plus sorted unique values.
function fillFacet(sel, allLabel, values) {
  const all = document.createElement("option");
  all.value = "";
  all.textContent = allLabel;
  sel.appendChild(all);
  const unique = [...new Set(values.filter(Boolean))].sort((a, b) => a.localeCompare(b));
  for (const v of unique) {
    const opt = document.createElement("option");
    opt.value = v;
    opt.textContent = v;
    sel.appendChild(opt);
  }
}

// fillNavCounts writes entity counts into the sidebar once the directory
// loads. A failed load just leaves the nav plain.
async function fillNavCounts() {
  try {
    const dir = await directory();
    for (const span of document.querySelectorAll(".nav-count")) {
      span.textContent = (dir[span.dataset.count] || []).length;
    }
  } catch (err) {
    // The nav works without counts.
  }
}

async function renderDirectory(view) {
  dirStatus.textContent = "Loading...";
  dirList.replaceChildren();
  let dir;
  try {
    dir = await directory();
  } catch (err) {
    dirStatus.textContent = "Could not load the directory: " + err.message;
    return;
  }
  if (currentView !== view) return;

  const rows = dir[view] || [];
  if (view === "people") fillFacets(rows);
  const q = dirFilter.value.trim().toLowerCase();
  let shown = q ? rows.filter((r) => rowText(r).includes(q)) : rows;
  if (view === "people") {
    if (facetTeam.value) shown = shown.filter((r) => r.team === facetTeam.value);
    if (facetOrg.value) shown = shown.filter((r) => r.org === facetOrg.value);
  }

  const sorts = fillSorts(view);
  const chosen = sorts.find(([k]) => k === dirSort?.value) || sorts[0];
  if (chosen) shown = shown.slice().sort(chosen[2]);
  fillExport(view, () => shown);

  dirList.replaceChildren();
  for (const r of shown) dirList.appendChild(directoryRow(view, r));
  if (rows.length === 0) {
    dirStatus.textContent = DIR_VIEWS[view].empty;
  } else if (shown.length !== rows.length) {
    dirStatus.textContent = shown.length + " of " + rows.length;
  } else {
    dirStatus.textContent = String(rows.length);
  }
}

// DIR_SORTS are the orderings each view offers, first one being its default.
// A list of a few hundred rows sorted one way answers one question; sorting is
// what lets the same list answer "what is biggest", "what is alphabetical", and
// "what has nobody left on it".
const DIR_SORTS = {
  people: [
    ["name", "Name", (a, b) => cmpText(a.name, b.name)],
    ["subjects", "Most subjects", (a, b) => (b.topics || []).length - (a.topics || []).length],
    ["team", "Team", (a, b) => cmpText(a.team, b.team) || cmpText(a.name, b.name)],
  ],
  topics: [
    ["people", "Most people", (a, b) => b.people - a.people || cmpText(a.name, b.name)],
    ["name", "Name", (a, b) => cmpText(a.name, b.name)],
    ["thin", "Fewest people", (a, b) => a.people - b.people || cmpText(a.name, b.name)],
    ["stale", "Nobody active", (a, b) => (a.active || 0) - (b.active || 0) || cmpText(a.name, b.name)],
  ],
  teams: [
    ["people", "Most people", (a, b) => b.people - a.people || cmpText(a.name, b.name)],
    ["name", "Name", (a, b) => cmpText(a.name, b.name)],
  ],
  channels: [
    ["members", "Most members", (a, b) => b.members - a.members || cmpText(a.name, b.name)],
    ["name", "Name", (a, b) => cmpText(a.name, b.name)],
  ],
};

// DIR_COLUMNS are what each view exports, so a download is the table the reader
// is looking at rather than a dump of the whole index.
const DIR_COLUMNS = {
  people: [
    ["Name", (r) => r.name || r.id],
    ["Email", (r) => r.email],
    ["Title", (r) => r.title],
    ["Team", (r) => r.team],
    ["Org", (r) => r.org],
    ["Knows", (r) => (r.topics || []).join("; ")],
  ],
  topics: [
    ["Subject", (r) => r.name],
    ["People", (r) => r.people],
    ["Still active", (r) => r.active],
    ["Leads", (r) => r.lead],
  ],
  teams: [
    ["Team", (r) => r.name],
    ["Org", (r) => r.org],
    ["People", (r) => r.people || "\u2014"],
    ["Works on", (r) => (r.topics || []).join("; ")],
  ],
  channels: [
    ["Channel", (r) => r.name],
    ["Topic", (r) => r.topic],
    ["Members", (r) => r.members],
  ],
};

// cmpText orders two possibly-missing strings the way a reader expects.
function cmpText(a, b) {
  return String(a || "").toLowerCase().localeCompare(String(b || "").toLowerCase());
}

// fillSorts puts the orderings for a view in the dropdown, keeping the current
// choice when the reader switches back to a view that has it.
function fillSorts(view) {
  const sorts = DIR_SORTS[view] || [];
  if (!dirSort) return sorts;
  const keep = dirSort.value;
  dirSort.replaceChildren();
  for (const [key, label] of sorts) {
    const opt = document.createElement("option");
    opt.value = key;
    opt.textContent = label;
    dirSort.appendChild(opt);
  }
  if (sorts.some(([k]) => k === keep)) dirSort.value = keep;
  dirSort.hidden = sorts.length < 2;
  return sorts;
}

// fillExport keeps one export button in the controls, pointed at whatever the
// current view is showing right now.
function fillExport(view, rows) {
  if (!dirControls) return;
  const cols = DIR_COLUMNS[view];
  const existing = dirControls.querySelector(".dir-export");
  if (existing) existing.remove();
  if (!cols) return;
  const button = exportButton("Export CSV", () => {
    const shown = rows();
    downloadCSV(
      "peepwho-" + view + ".csv",
      cols.map(([h]) => h),
      shown.map((r) => cols.map(([, get]) => get(r))),
    );
  });
  button.classList.add("dir-export");
  dirControls.appendChild(button);
}

// rowText flattens a directory row for filtering.
function rowText(r) {
  return [r.name, r.email, r.title, r.team, r.topic, r.org, ...(r.topics || [])]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

function directoryRow(view, r) {
  switch (view) {
    case "people":
      return dirPersonRow(r);
    case "channels":
      return dirChannelRow(r);
    case "teams":
      return dirTeamRow(r);
    default:
      return dirTopicRow(r);
  }
}

// dirPersonRow is one person in the roster. A directory is read by scanning
// hundreds of rows, not by studying one, so this is a dense row rather than a
// card: face, who they are, what they know, and a copy that stays out of the
// way until the row is under the pointer.
function dirPersonRow(p) {
  const card = el("div", "card person-card dir-row");
  const head = el("div", "person-head");
  head.appendChild(personAvatar(p.name || p.id || ""));

  const who = el("div", "person-who");
  const name = el("div", "name");
  const toggle = el("button", "name-toggle", p.name || p.id);
  toggle.type = "button";
  toggle.title = "Show everything peepwho knows";
  toggle.addEventListener("click", () => openProfile(p.id));
  name.appendChild(toggle);
  who.appendChild(name);
  const sub = [p.title, p.team, p.org].filter(Boolean).join(" · ");
  if (sub) who.appendChild(el("div", "sub", sub));
  head.appendChild(who);

  const contact = el("div", "person-contact");
  if (p.email) contact.appendChild(el("span", "person-mail", p.email));
  // Everything peepwho knows about them, in one paste: for a ticket, a handover
  // note, or the message you are about to write asking for help.
  contact.appendChild(copyButton(() => [
    p.name || p.id,
    [p.title, p.team, p.org].filter(Boolean).join(" · "),
    p.email || "",
    (p.topics || []).length ? "Knows: " + (p.topics || []).join(", ") : "",
  ].filter(Boolean).join("\n"), "copy"));
  head.appendChild(contact);
  card.appendChild(head);
  chips(card, p.topics);
  return card;
}

function dirChannelRow(c) {
  const card = el("div", "card");
  const name = el("div", "name", "#" + c.name);
  name.appendChild(copyButton("#" + c.name));
  card.appendChild(name);
  if (c.topic) card.appendChild(el("div", "sub", c.topic));
  card.appendChild(el("div", "sub", c.members + " active " + (c.members === 1 ? "person" : "people")));
  return card;
}

// activate makes a whole card act as one button: click or Enter/Space anywhere
// on it fires the action, not just an inner label.
function activate(node, fn) {
  node.addEventListener("click", fn);
  node.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      fn();
    }
  });
}

function dirTeamRow(t) {
  const card = el("div", "card dir-clickable");
  card.setAttribute("role", "button");
  card.tabIndex = 0;
  card.title = "Show this team's people";
  card.appendChild(el("div", "name", t.name));
  const sub = [t.org, t.people + (t.people === 1 ? " person" : " people")].filter(Boolean).join(" · ");
  if (sub) card.appendChild(el("div", "sub", sub));
  // What the team actually works on, so the row says something about the team
  // rather than only counting it.
  if (t.topics && t.topics.length) {
    card.appendChild(el("p", "exp-also", t.topics.join(", ")));
  }
  activate(card, () => {
    pendingTeamFacet = t.name;
    dirFilter.value = "";
    if (location.hash === "#/people") renderDirectory("people");
    else location.hash = "#/people";
  });
  return card;
}

function dirTopicRow(t) {
  const card = el("div", "card card-row dir-clickable");
  card.setAttribute("role", "button");
  card.tabIndex = 0;
  card.title = "Ask who knows about this";
  card.appendChild(el("span", "name", t.name));
  // Who to ask, without a second click. A count alone makes every row look the
  // same and answers nothing.
  if (t.lead) {
    // Say what the name is. A person's name floating mid-row is read as a
    // label for the row rather than as the answer to "who leads this".
    const lead = el("span", "sub row-lead");
    lead.appendChild(el("span", "row-lead-label", "leads"));
    lead.appendChild(document.createTextNode(t.lead));
    card.appendChild(lead);
  }
  const people = t.people + (t.people === 1 ? " person" : " people");
  // Nobody working on something several people know is the finding worth
  // seeing in a list, so it is said rather than left to arithmetic.
  const active = t.active === 0 ? "none active" : t.active + " active";
  card.appendChild(el("span", "count", people + " · " + active));
  activate(card, () => runAsk(t.name));
  return card;
}

dirFilter.addEventListener("input", () => {
  if (currentView !== "ask") renderDirectory(currentView);
});
if (dirSort) {
  dirSort.addEventListener("change", () => {
    if (currentView !== "ask") renderDirectory(currentView);
  });
}
facetTeam.addEventListener("change", () => renderDirectory("people"));
facetOrg.addEventListener("change", () => renderDirectory("people"));

// Recall opens straight to your recent conversations, no typing required. The
// topic box and the source and time controls are optional refinements.
function openRecall() {
  runRecall();
}

function scheduleRecall() {
  if (recallTimer) clearTimeout(recallTimer);
  recallTimer = setTimeout(runRecall, 300);
}

if (recallQuery) recallQuery.addEventListener("input", scheduleRecall);
if (recallMe) recallMe.addEventListener("input", scheduleRecall);
if (recallSince) recallSince.addEventListener("change", applyRecallFilters);

// runRecall fetches the conversations you took part in. With no topic it
// returns your most recent ones; a topic narrows them. It then rebuilds the
// source filter and renders through applyRecallFilters.
async function runRecall() {
  const me = recallMe.value.trim();
  const query = recallQuery.value.trim();
  if (!me) {
    recallStatus.textContent = "Set who you are to see your conversations.";
    recallList.replaceChildren();
    if (recallSources) recallSources.replaceChildren();
    return;
  }
  recallStatus.textContent = "Looking\u2026";
  try {
    const res = await fetch(
      "/api/recall?me=" + encodeURIComponent(me) +
      (query ? "&q=" + encodeURIComponent(query) : "") + "&limit=25");
    const data = await res.json();
    if (!res.ok) {
      recallStatus.textContent = data.error || "Recall is unavailable.";
      recallList.replaceChildren();
      if (recallSources) recallSources.replaceChildren();
      return;
    }
    recallData = data;
    buildRecallSources(data);
    applyRecallFilters();
  } catch (err) {
    recallStatus.textContent = "Recall is unavailable.";
  }
}

// buildRecallSources renders a checkbox per source present in the results so
// you can narrow to the tools you care about. One source needs no filter.
function buildRecallSources(data) {
  if (!recallSources) return;
  const eps = data.episodes || [];
  const sources = [...new Set(eps.map((e) => e.source).filter(Boolean))].sort();
  recallSources.replaceChildren();
  recallSourceFilter = null;
  if (recallSlabel) recallSlabel.hidden = sources.length < 2;
  if (sources.length < 2) return;
  for (const src of sources) {
    const meta = sourceMeta(src);
    const chip = el("label", "recall-src");
    chip.style.setProperty("--sc", meta.color);
    const box = document.createElement("input");
    box.type = "checkbox";
    box.checked = true;
    box.value = src;
    box.addEventListener("change", onRecallSourceToggle);
    chip.appendChild(box);
    chip.appendChild(el("span", "rc-dot"));
    chip.appendChild(el("span", null, meta.label));
    recallSources.appendChild(chip);
  }
}

function onRecallSourceToggle() {
  const boxes = recallSources.querySelectorAll("input[type=checkbox]");
  const on = new Set();
  let allOn = true;
  for (const b of boxes) {
    if (b.checked) on.add(b.value);
    else allOn = false;
  }
  recallSourceFilter = allOn ? null : on;
  applyRecallFilters();
}

// applyRecallFilters renders the fetched conversations narrowed by the source
// checkboxes and the time dropdown, without re-querying the server.
function applyRecallFilters() {
  if (!recallData) return;
  let eps = recallData.episodes || [];
  const total = eps.length;
  const days = parseInt(recallSince ? recallSince.value : "0", 10) || 0;
  if (days > 0) {
    const cutoff = Date.now() - days * 86400000;
    eps = eps.filter((e) => !e.when || new Date(e.when).getTime() >= cutoff);
  }
  if (recallSourceFilter) {
    eps = eps.filter((e) => recallSourceFilter.has(e.source));
  }
  if (recallCount) {
    recallCount.textContent = !total ? ""
      : eps.length === total ? total + (total === 1 ? " conversation" : " conversations")
      : eps.length + " of " + total;
  }
  recallScope.textContent = (recallData.scope && recallData.scope.note) || "";
  recallList.replaceChildren();
  if (!eps.length) {
    recallStatus.textContent = total
      ? "No conversations match these filters. Widen the date range or turn a source back on."
      : "Nothing found in the conversations you took part in. Recall only ever covers your own, so an empty result here usually means the sources you talk in are not indexed yet.";
    return;
  }
  recallStatus.textContent = "";
  // A whole session leaves too, not just one card at a time. This is the half
  // of the product for people in the trenches, and "how did we solve this last
  // time" is an answer that goes into a runbook or a reply to somebody else.
  if (recallCount && !recallCount.querySelector(".copy")) {
    recallCount.appendChild(exportButton("Export", () => {
      const rows = currentRecallEpisodes().map((e) => [
        e.when || "", sourceMeta(e.source).label, e.title || "",
        (e.participants || []).map((p) => p.name || p.email || p).join("; "),
        e.summary || "", e.permalink || "",
      ]);
      downloadCSV("peepwho-recall.csv",
        ["When", "Source", "Title", "Who", "Summary", "Link"], rows);
    }));
  }
  shownRecall = eps;
  for (const ep of eps) recallList.appendChild(recallCard(ep));
}

// shownRecall is what the recall view is displaying right now, so an export
// matches the filters on screen rather than dumping everything fetched.
let shownRecall = [];

// currentRecallEpisodes returns what is on screen.
function currentRecallEpisodes() { return shownRecall; }

// recallCard builds the card for one remembered conversation.
// SOURCE_META gives each tool a distinct color and label, so a glance tells you
// where a conversation happened.
const SOURCE_META = {
  github: { label: "GitHub", color: "#a371f7" },
  slack: { label: "Slack", color: "#e01e5a" },
  pagerduty: { label: "PagerDuty", color: "#06ac38" },
  jira: { label: "Jira", color: "#2684ff" },
  confluence: { label: "Confluence", color: "#1d7afc" },
  gitlab: { label: "GitLab", color: "#fc6d26" },
  teams: { label: "Teams", color: "#6264a7" },
  linear: { label: "Linear", color: "#5e6ad2" },
  notion: { label: "Notion", color: "#b9b9b9" },
  zendesk: { label: "Zendesk", color: "#17a289" },
};

// sourceMeta returns the color and label for a source, or a neutral default.
function sourceMeta(src) {
  return SOURCE_META[(src || "").toLowerCase()] || { label: src || "source", color: "#8b93a3" };
}

// recallCard draws one conversation, colored and labeled by its source, with a
// clear place, who was there, and a button back to it.
function recallCard(ep) {
  const meta = sourceMeta(ep.source);
  const card = el("div", "card recall-card");
  card.style.setProperty("--sc", meta.color);

  const head = el("div", "rc-head");
  const src = el("span", "rc-source");
  src.appendChild(el("span", "rc-dot"));
  src.appendChild(el("span", null, meta.label));
  head.appendChild(src);
  if (ep.when) {
    head.appendChild(el("span", "rc-date", new Date(ep.when).toLocaleDateString(
      undefined, { year: "numeric", month: "short", day: "numeric" })));
  }
  card.appendChild(head);

  if (ep.place) {
    const place = ep.kind === "thread" || ep.kind === "window" ? "#" + ep.place : ep.place;
    card.appendChild(el("h3", "rc-title", place));
  }
  card.appendChild(recallPeopleLine(ep));

  // ep.matched carries the scorer's stems, and "bill retri" is tokenizer
  // underwear no reader should see. The card's title and participants already
  // say why this conversation surfaced.

  if (ep.solution) {
    const sol = el("div", "recall-solution");
    if (ep.solution.summary) sol.appendChild(el("p", "recall-summary", ep.solution.summary));
    for (const note of ep.solution.notes || []) {
      const line = el("p", "recall-note-line");
      line.appendChild(el("span", "recall-author", note.author + ": "));
      line.appendChild(document.createTextNode(note.text));
      sol.appendChild(line);
    }
    if (ep.solution.truncated) sol.appendChild(el("p", "recall-more", "The conversation ran longer."));
    card.appendChild(sol);
  }

  const foot = el("div", "rc-foot");
  if (ep.permalink) {
    head.appendChild(copyButton(ep.permalink, "copy link"));
  }
  if (ep.permalink) {
    const link = el("a", "rc-open");
    link.href = safeHref(ep.permalink);
    link.target = "_blank";
    link.rel = "noopener";
    link.appendChild(el("span", null, "Open in " + meta.label));
    link.appendChild(el("span", "rc-arrow", "\u2192"));
    foot.appendChild(link);
  }
  if (ep.link_may_have_expired) {
    foot.appendChild(el("span", "rc-stale", "link may be stale"));
  }
  if (foot.childNodes.length) card.appendChild(foot);

  return card;
}

// recallNames is everyone else in a conversation, in order.
function recallNames(ep) {
  return (ep.people || []).map((p) => p.name || p.email || p.id).filter(Boolean);
}

// recallPeople names who else was in a conversation. A busy channel thread has
// thirty participants, and printing all of them buries the conversation the
// card is about, so it names the first few and counts the rest. The full list
// is one click away rather than gone.
function recallPeople(ep) {
  const names = recallNames(ep);
  if (!names.length) return "On your own";
  if (names.length === 1) return "With " + names[0];
  if (names.length <= 4) {
    return "With " + names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
  }
  return "With " + names.slice(0, 3).join(", ") + " and " + (names.length - 3) + " others";
}

// recallPeopleLine builds the participants line, with the rest revealed in
// place when there are more than the summary names.
function recallPeopleLine(ep) {
  const names = recallNames(ep);
  const line = el("div", "rc-people", recallPeople(ep));
  if (names.length <= 4) return line;
  const more = el("button", "rc-more", "show all " + names.length);
  more.type = "button";
  let open = false;
  more.addEventListener("click", () => {
    open = !open;
    line.firstChild.textContent = open
      ? "With " + names.slice(0, -1).join(", ") + " and " + names[names.length - 1]
      : recallPeople(ep);
    more.textContent = open ? "show fewer" : "show all " + names.length;
  });
  const wrap = el("div", "rc-people-wrap");
  const text = el("span", "", line.textContent);
  line.replaceChildren(text);
  wrap.appendChild(line);
  wrap.appendChild(more);
  return wrap;
}


// renderExposure draws where the organization is exposed: topics whose
// knowledge sits in too few people, and areas whose declared owner is not the
// one doing the work. Both come from /api/exposure, computed over the graph.
async function renderExposure() {
  expStatus.textContent = "Loading...";
  expRisk.replaceChildren();
  expDrift.replaceChildren();
  if (expRegions) expRegions.replaceChildren();
  let data;
  try {
    const res = await fetch("/api/exposure", { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error("HTTP " + res.status);
    data = await res.json();
  } catch (err) {
    expStatus.textContent = "Could not load exposure: " + err.message;
    return;
  }
  if (currentView !== "exposure") return;

  const places = data.places || [];
  const risk = data.risk || [];
  const drift = data.drift || [];
  const regions = data.regions || [];
  const spans = data.spans || [];
  const crit = risk.filter((r) => r.level === "critical").length;
  const elev = risk.filter((r) => r.level === "elevated").length;
  expStatus.textContent =
    (places.length ? places.length + " systems \u00b7 " : "") +
    risk.length + " topics scored";

  // The four numbers first, each a way into the section that explains it. A
  // reader who came to find out whether they have a problem should not have to
  // read three paragraphs to see the count.
  const cards = document.getElementById("exp-cards");
  if (cards) {
    cards.replaceChildren();
    const stat = (n, label, tone, target) => {
      const c = el("button", "exp-stat exp-stat-" + tone);
      c.type = "button";
      c.appendChild(el("span", "exp-stat-n", String(n)));
      c.appendChild(el("span", "exp-stat-l", label));
      c.addEventListener("click", () => {
        const to = document.getElementById(target);
        if (to) to.scrollIntoView({ behavior: "smooth", block: "start" });
      });
      return c;
    };
    // Places lead when there are any: a directory is checkable against the
    // repository the reader already has, and a topic is not.
    if (places.length) {
      const sole = places.filter((p) => (p.bus || 0) <= 1).length;
      // No place resting on one person is a good result, so it is not drawn
      // in the alarming color: a red zero reads as a broken counter.
      cards.appendChild(stat(sole,
        sole === 1 ? "system on one person" : "systems on one person",
        sole ? "crit" : "warn", "exp-sec-places"));
    }
    cards.appendChild(stat(crit, crit === 1 ? "critical subject" : "critical subjects",
      "crit", "exp-sec-risk"));
    cards.appendChild(stat(elev, "elevated", "elev", "exp-sec-risk"));
    cards.appendChild(stat(regions.length,
      regions.length === 1 ? "joined body of work" : "joined bodies of work",
      "warn", "exp-sec-regions"));
    cards.appendChild(stat(spans.length,
      spans.length === 1 ? "one-person connection" : "one-person connections",
      "warn", "exp-sec-spans"));
    cards.appendChild(stat(drift.length, "ownership drifted", "drift", "exp-sec-drift"));
  }

  // The places section hides entirely when no repository was indexed, rather
  // than showing an empty list: there is no finding to be missing.
  const expPlaces = document.getElementById("exp-places");
  const placeHead = document.getElementById("exp-sec-places");
  const placeNote = document.getElementById("exp-note-places");
  if (expPlaces) {
    expPlaces.replaceChildren();
    if (placeHead) placeHead.hidden = !places.length;
    if (placeNote) placeNote.hidden = !places.length;
    for (const p of places) expPlaces.appendChild(placeCard(p));
  }

  if (expRegions) {
    if (regions.length) {
      for (const r of regions) expRegions.appendChild(regionCard(r));
    } else {
      expRegions.appendChild(el("p", "exp-empty",
        "No joined work found. Index git, Jira, Confluence, or GitHub: those record which subjects one piece of work touched."));
    }
  }
  // The findings are what goes into a planning conversation, so they leave as a
  // file rather than as a screenshot of a screen somebody had open once.
  if (expStatus && expStatus.parentElement && !expStatus.parentElement.querySelector(".exp-export")) {
    const b = exportButton("Export findings", () => {
      const out = [];
      for (const p of places) {
        out.push(["System", p.dir, (p.bus || 0) <= 1 ? "sole" : "", p.bus,
          (p.holders || []).map((h) =>
            h.name + " " + Math.round(100 * (h.share || 0)) + "%").join("; ")]);
      }
      for (const r of risk) {
        out.push(["Concentration", r.topic, r.level || "", r.busFactor,
          (r.experts || []).map((e) => e.name + " " + Math.round((e.share || 0) * 100) + "%").join("; ")]);
      }
      for (const s of (data.spans || [])) {
        out.push(["One-person connection", (s.topics || []).join(" + "), "", s.experts, s.person]);
      }
      for (const r of (data.regions || [])) {
        out.push(["Joined work", (r.topics || []).join("; "), "", (r.topics || []).length, r.lead]);
      }
      for (const d of drift) {
        out.push(["Ownership drift", d.topic, d.why || "", "", "declared " +
          (d.declared || []).join("; ") + "; actually " + (d.actual || "")]);
      }
      downloadCSV("peepwho-findings.csv",
        ["Finding", "Subject", "Detail", "Count", "People"], out);
    });
    b.classList.add("exp-export");
    expStatus.parentElement.appendChild(b);
  }
  if (expSpans) {
    const spans = data.spans || [];
    if (spans.length) {
      // The strongest finding gets drawn, not just listed. Two healthy areas
      // and a single dot between them says in one glance what the cards say
      // in three sentences, and it is the picture a reader carries out.
      expSpans.appendChild(crossingFigure(spans[0]));
      for (const sp of spans) expSpans.appendChild(spanCard(sp));
    } else {
      expSpans.appendChild(el("p", "exp-empty",
        "No one-person connections found. Index git, Jira, Confluence, or GitHub: those record which subjects one piece of work touched."));
    }
  }
  if (risk.length) {
    for (const r of risk) expRisk.appendChild(riskCard(r));
  } else {
    expRisk.appendChild(el("p", "exp-empty", "No topics scored yet. Index a source with expertise signal first."));
  }
  if (drift.length) {
    for (const d of drift) expDrift.appendChild(driftCard(d));
  } else {
    expDrift.appendChild(el("p", "exp-empty",
      "No ownership drift found. If that seems unlikely, nothing here declares ownership yet: "
      + "index a CODEOWNERS file to compare what is written down against who does the work."));
  }
  fillDeparturePeople();
  // A departure check is shareable the same way a query is: /?dep=Gavin+Hudson
  // opens the exposure view with that person already checked.
  const params = new URLSearchParams(location.search);
  const dep = params.get("dep");
  if (dep && expDepResult && !expDepResult.childElementCount) checkDeparture(dep);
  if (params.get("proof") && expProofOut && !expProofOut.childElementCount) sealFinding();
}

// fillDeparturePeople stocks the person picker from the directory, once, so a
// name can be chosen rather than remembered.
async function fillDeparturePeople() {
  if (!expDepPeople || expDepPeople.childElementCount) return;
  try {
    const dir = await directory();
    for (const p of (dir.people || []).slice(0, 500)) {
      const opt = document.createElement("option");
      opt.value = p.name || p.id;
      expDepPeople.appendChild(opt);
    }
  } catch (err) {
    // The picker is a convenience; typing a name still works without it.
  }
}

// checkDeparture asks what one person's departure would cost and draws it.
async function checkDeparture(who) {
  const person = (who || "").trim();
  if (!person) return;
  if (expDepInput) expDepInput.value = person;
  expDepResult.replaceChildren();
  expDepResult.appendChild(el("p", "exp-empty", "Checking..."));
  let imp;
  try {
    const res = await fetch("/api/departure?person=" + encodeURIComponent(person),
      { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error("HTTP " + res.status);
    imp = await res.json();
  } catch (err) {
    expDepResult.replaceChildren();
    expDepResult.appendChild(el("p", "exp-empty", "Could not check that person: " + err.message));
    return;
  }
  expDepResult.replaceChildren();
  if (!imp.person) {
    expDepResult.appendChild(el("p", "exp-empty", "Nobody in the graph matches " + person + "."));
    return;
  }
  const sole = imp.sole || [];
  const top = imp.top || [];
  const card = el("div", "exp-card " + (sole.length ? "exp-critical" : top.length ? "exp-elevated" : "exp-ok"));
  const head = el("div", "exp-card-head");
  head.appendChild(el("span", "exp-topic", imp.name || imp.person));
  head.appendChild(el("span", "exp-level", sole.length ? "sole owner" : top.length ? "top expert" : "covered"));
  head.appendChild(el("span", "exp-bus", sole.length + " sole, " + top.length + " leading"));
  // What leaves with somebody is the one finding here that gets written down
  // somewhere else: a handover note, a succession conversation, a backfill
  // request. It copies as prose rather than as a list of topic names, because
  // that is what the person reading it three weeks later needs.
  head.appendChild(copyButton(() => {
    const name = imp.name || imp.person;
    const lines = [];
    if (sole.length) {
      lines.push("If " + name + " leaves, nobody else has any expertise in "
        + sole.length + " area" + (sole.length === 1 ? "" : "s") + ": " + sole.join(", ") + ".");
    }
    if (top.length) {
      lines.push(name + " is the strongest expert in " + top.join(", ")
        + ", though others remain.");
    }
    if (!sole.length && !top.length) {
      lines.push(name + " leads nothing on their own. Every area they work in has another expert.");
    }
    return lines.join("\n\n");
  }, "copy handover"));
  card.appendChild(head);
  if (sole.length) {
    card.appendChild(el("p", "exp-also", "Nobody else has any expertise in these:"));
    chips(card, sole);
  }
  if (top.length) {
    card.appendChild(el("p", "exp-also", "Strongest expert, but others remain:"));
    chips(card, top);
  }
  if (!sole.length && !top.length) {
    card.appendChild(el("p", "exp-also", "Leads nothing on their own. Their areas all have another expert."));
  }
  expDepResult.appendChild(card);
}

// regionCard draws one joined body of work and who it rests on. The subjects
// are listed in full rather than counted, because the point of the finding is
// how much one person would take with them.
// placeCard draws one directory and the people its work rests on. Share is
// each holder's part of the directory's credited work, so a single holder at
// a high share is the finding: one person, nobody behind them.
function placeCard(p) {
  const holders = p.holders || [];
  const sole = (p.bus || 0) <= 1;
  const card = el("div", "exp-card " + (sole ? "exp-critical" : "exp-elevated"));
  const head = el("div", "exp-card-head");
  head.appendChild(el("span", "exp-topic", p.dir));
  head.appendChild(el("span", "exp-bus",
    sole ? "rests on one person" : p.bus + " of " + p.people + " cover it"));
  head.appendChild(copyButton(() =>
    p.dir + " rests on " + holders.map((h) => h.name).join(", ")));
  card.appendChild(head);
  const line = el("p", "exp-also");
  holders.forEach((h, i) => {
    if (i) line.appendChild(document.createTextNode(", "));
    line.appendChild(personLink(h.name, h.id));
    const share = Math.round(100 * (h.share || 0));
    if (share) line.appendChild(document.createTextNode(" " + share + "%"));
  });
  card.appendChild(line);
  return card;
}

function regionCard(r) {
  const topics = r.topics || [];
  const card = el("div", "exp-card exp-critical");
  const head = el("div", "exp-card-head");
  const lead = el("span", "exp-topic");
  lead.appendChild(personLink(r.lead, r.leadId));
  head.appendChild(lead);
  head.appendChild(el("span", "exp-bus", topics.length + " joined subjects"));
  head.appendChild(copyButton(() =>
    r.lead + " leads all " + topics.length + " of these, and they move together: " + topics.join(", ")));
  card.appendChild(head);
  card.appendChild(el("p", "exp-also", topics.join(", ")));
  return card;
}

// spanCard draws one connection that rests on a single person. It names how
// many people hold the two subjects between them, because that is what makes
// the finding surprising: the subjects are not short of experts, the crossing
// between them is.
// crossingFigure draws one sole-person connection: each area healthy on its
// own, the only line between them running through one person. Drawn from the
// live finding, so the names are whatever the graph actually says.
function crossingFigure(sp) {
  const topics = sp.topics || [];
  const a = topics[0] || "";
  const b = topics[topics.length - 1] || "";
  const fig = el("figure", "crossfig");
  const half = Math.max(1, Math.floor((sp.experts || 2) / 2));
  const dots = (cx) => {
    let out = "";
    const spots = [[-26, -14], [12, -22], [-10, 18], [26, 10]];
    for (let i = 0; i < Math.min(4, half); i++) {
      out += '<circle cx="' + (cx + spots[i][0]) + '" cy="' + (72 + spots[i][1]) + '" r="5"/>';
    }
    return out;
  };
  fig.innerHTML =
    '<svg viewBox="0 0 560 148" role="img" aria-label="' + escapeAttr(sp.person) +
    ' is the only person connecting ' + escapeAttr(a) + ' and ' + escapeAttr(b) + '">' +
    '<g class="cf-area"><ellipse cx="112" cy="72" rx="88" ry="50"/>' + dots(112) +
    '<text x="112" y="140">' + escapeHTML(shorten(a)) + '</text></g>' +
    '<g class="cf-area"><ellipse cx="448" cy="72" rx="88" ry="50"/>' + dots(448) +
    '<text x="448" y="140">' + escapeHTML(shorten(b)) + '</text></g>' +
    '<path class="cf-link" d="M200 72 L360 72"/>' +
    '<circle class="cf-one" cx="280" cy="72" r="9"/>' +
    '<text class="cf-lab" x="280" y="50">' + escapeHTML(sp.person) + '</text>' +
    "</svg>";
  const cap = el("figcaption", null,
    (sp.experts || 0) + " people hold these areas. The crossing is " + sp.person + " alone.");
  fig.appendChild(cap);
  return fig;
}

// shorten keeps an area name readable inside the figure.
function shorten(t) {
  return t.length > 22 ? t.slice(0, 21) + "\u2026" : t;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
}
function escapeAttr(s) { return escapeHTML(s); }

function spanCard(sp) {
  const card = el("div", "exp-card exp-critical");
  const head = el("div", "exp-card-head");
  const topics = sp.topics || [];
  head.appendChild(el("span", "exp-topic", topics.join(" + ")));
  const only = el("span", "exp-bus");
  only.title = "Counted from focused changes. A sweeping refactor touching "
    + "hundreds of areas at once is not evidence anyone understands how they connect.";
  only.appendChild(document.createTextNode("only "));
  only.appendChild(personLink(sp.person, sp.personId));
  head.appendChild(only);
  head.appendChild(copyButton(() =>
    sp.person + " is the only person who has worked across " + topics.join(" and ")
      + ", though " + (sp.experts || 0) + " people hold those subjects individually"));
  card.appendChild(head);
  only.appendChild(el("span", "exp-dim", " · " + (sp.experts || 0) + " hold them"));
  return card;
}

// riskCard draws one topic's knowledge concentration with its experts.
function riskCard(r) {
  const card = el("div", "exp-card exp-" + (r.level || "ok"));
  const head = el("div", "exp-card-head");
  head.appendChild(el("span", "exp-topic", r.topic));
  head.appendChild(el("span", "exp-level", r.level || "ok"));
  head.appendChild(el("span", "exp-bus", "bus factor " + r.busFactor));
  head.appendChild(copyButton(() => {
    const who = (r.experts || []).map((e) => e.name + " " + Math.round(e.share * 100) + "%");
    return r.topic + ": bus factor " + r.busFactor
      + (who.length ? ", held by " + who.join(", ") : "");
  }));
  card.appendChild(head);
  if (r.includes && r.includes.length) {
    card.appendChild(el("p", "exp-also", "also called " + r.includes.join(", ")));
  }
  addRelated(card, r.topic);
  for (const e of r.experts || []) {
    const row = el("div", "exp-expert");
    row.appendChild(el("span", "exp-share", Math.round(e.share * 100) + "%"));
    const bar = el("span", "exp-bar");
    const fill = el("span", "exp-bar-fill");
    fill.style.width = Math.round(e.share * 100) + "%";
    bar.appendChild(fill);
    row.appendChild(bar);
    const name = el("button", "exp-name exp-name-btn", e.name);
    name.type = "button";
    name.title = "See what leaves with " + e.name;
    name.addEventListener("click", () => checkDeparture(e.id || e.name));
    row.appendChild(name);
    card.appendChild(row);
  }
  return card;
}

// driftCard draws one area where the declared owner is not the real expert.
function driftCard(d) {
  const card = el("div", "exp-card exp-drift-card");
  const head = el("div", "exp-card-head");
  head.appendChild(el("span", "exp-topic", d.topic));
  head.appendChild(copyButton(() =>
    d.topic + " is declared to " + (d.declared || []).join(", ")
      + " but the work is " + d.actual + "'s"));
  card.appendChild(head);
  const row = el("div", "exp-expert");
  row.appendChild(el("span", "exp-dim", "declared"));
  const declared = el("span", "exp-declared");
  (d.declared || []).forEach((name, i) => {
    if (i) declared.appendChild(document.createTextNode(", "));
    declared.appendChild(personLink(name, (d.declaredIds || [])[i]));
  });
  row.appendChild(declared);
  row.appendChild(el("span", "exp-dim", "actual"));
  const actual = el("span", "exp-actual");
  actual.appendChild(personLink(d.actual, d.actualId));
  row.appendChild(actual);
  card.appendChild(row);
  return card;
}


// addRelated hangs the topics that share this one's experts under its card, so a
// subject and its specialties read as one body of knowledge rather than as
// unrelated rows that happen to sit near each other.
async function addRelated(card, topic) {
  try {
    const res = await fetch("/api/related?topic=" + encodeURIComponent(topic) + "&limit=4",
      { headers: { Accept: "application/json" } });
    if (!res.ok) return;
    const data = await res.json();
    const rel = (data.related || []).filter((r) => r.topic !== topic);
    if (!rel.length) return;
    const line = el("p", "exp-also");
    line.appendChild(el("span", "exp-dim", "shares experts with "));
    rel.forEach((r, i) => {
      if (i) line.appendChild(el("span", "exp-dim", ", "));
      const t = el("span", "exp-rel", r.topic);
      t.title = Math.round(r.overlap * 100) + "% of its experts also hold " + topic +
        (r.narrower ? ", and it is the narrower of the two" : "");
      line.appendChild(t);
    });
    card.appendChild(line);
  } catch (err) {
    // Related topics are extra context; a card without them is still complete.
  }
}

// sealFinding asks the server to sign the current finding and shows the
// manifest that came back, plus the command that checks it without trusting
// this page.
async function sealFinding() {
  expProofOut.replaceChildren();
  expProofOut.appendChild(el("p", "exp-empty", "Signing..."));
  let manifest;
  try {
    const res = await fetch("/api/attest", { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error("HTTP " + res.status);
    manifest = await res.json();
  } catch (err) {
    expProofOut.replaceChildren();
    expProofOut.appendChild(el("p", "exp-empty", "Could not seal the finding: " + err.message));
    return;
  }
  const claim = manifest.claim || {};
  const files = manifest.files || [];
  const report = files[0] || {};

  const card = el("div", "exp-card exp-ok");
  const head = el("div", "exp-card-head");
  head.appendChild(el("span", "exp-topic", claim.type || "peepwho.knowledge-risk/1"));
  head.appendChild(el("span", "exp-level", "signed"));
  head.appendChild(el("span", "exp-bus", manifest.format || ""));
  card.appendChild(head);

  const rows = [
    ["signing key", manifest.key_id || ""],
    ["files sealed", String(files.length)],
    ["report sha256", report.sha256 || ""],
    ["sealed at", manifest.created_at || ""],
  ];
  for (const [k, v] of rows) {
    if (!v) continue;
    const row = el("div", "exp-expert");
    row.appendChild(el("span", "exp-dim", k));
    row.appendChild(el("span", "exp-mono", v));
    card.appendChild(row);
  }
  expProofOut.replaceChildren(card);

  const dl = el("a", "exp-dl", "Download the sealed finding");
  dl.href = "/api/attest?download=1";
  dl.setAttribute("download", "peepwho-finding.zip");
  expProofOut.appendChild(dl);
  const how = el("p", "exp-also",
    "Verify it anywhere, offline: unzip peepwho-finding.zip, then peepwho verify finding");
  expProofOut.appendChild(how);
}


// recordedRuns loads the transcripts of commands this demo cannot run itself,
// fetched once. A run that needs a model is recorded rather than faked, so what
// is on screen is what the command actually printed somewhere.
let recordedCache = null;
async function recordedRuns() {
  if (recordedCache) return recordedCache;
  const res = await fetch("/static/recorded.json", { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error("HTTP " + res.status);
  recordedCache = await res.json();
  return recordedCache;
}

// renderCLI prints one command's real terminal output. The tabs are built once
// and the selected one is fetched fresh, so what appears is what the command
// prints right now against this index.
async function renderCLI(cmd) {
  cliCurrent = cmd;
  if (!cliTabs.childElementCount) {
    for (const c of CLI_COMMANDS) {
      const b = el("button", "cli-tab" + (c.recorded ? " cli-tab-rec" : ""), c.label);
      b.type = "button";
      if (c.recorded) b.title = "Recorded: this run needed a model, which the demo does not run";
      b.dataset.cmd = c.cmd;
      b.addEventListener("click", () => renderCLI(c.cmd));
      cliTabs.appendChild(b);
    }
  }
  for (const b of cliTabs.children) {
    b.classList.toggle("active", b.dataset.cmd === cmd);
  }
  cliStatus.textContent = "Running...";
  cliOut.textContent = "";
  cliNote.textContent = "";
  cliNote.hidden = true;
  const spec = CLI_COMMANDS.find((c) => c.cmd === cmd) || {};
  if (spec.recorded) {
    try {
      const rec = await recordedRuns();
      const run = rec[cmd];
      if (!run) throw new Error("not recorded");
      if (cliCurrent !== cmd) return;
      cliOut.textContent = run.text;
      cliNote.textContent = run.note;
      cliNote.hidden = false;
      cliStatus.textContent = "recorded, not live";
    } catch (err) {
      cliStatus.textContent = "Could not load that: " + err.message;
    }
    return;
  }
  try {
    const res = await fetch("/api/cli?cmd=" + encodeURIComponent(cmd), {
      headers: { Accept: "text/plain" },
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const text = await res.text();
    if (cliCurrent !== cmd) return;
    const label = (CLI_COMMANDS.find((c) => c.cmd === cmd) || {}).label || cmd;
    cliOut.textContent = "$ " + label + "\n\n" + text;
    cliStatus.textContent = "";
  } catch (err) {
    cliStatus.textContent = "Could not run that: " + err.message;
  }
}

if (expProofGo) {
  expProofGo.addEventListener("click", sealFinding);
}
if (expDepGo) {
  expDepGo.addEventListener("click", () => checkDeparture(expDepInput.value));
}
if (expDepInput) {
  expDepInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      checkDeparture(expDepInput.value);
    }
  });
}

window.addEventListener("hashchange", () => showView(viewFromHash()));

// A shared link carries the question and person in the URL; run them on load.
const linked = new URLSearchParams(location.search);
if (linked.get("q")) {
  qInput.value = linked.get("q");
  ask();
}
if (linked.get("person")) {
  openProfile(linked.get("person"));
}
// A fresh landing runs the example question by itself. The placeholder used to
// sit in an empty box waiting for a click, and a visitor reads that as a
// mock-up rather than a working instance: the first thing the page should do
// is what the page is for.
if (!linked.get("q") && !linked.get("person") && viewFromHash() === "ask") {
  qInput.value = qInput.placeholder;
  ask();
}
showView(viewFromHash());
// Examples come from the index, so they run once the directory helpers below
// exist: called any earlier they hit the uninitialized cache and fall back.
showExamples();
fillNavCounts();
loadModes();

// The mobile menu: one solid button, one solid sheet. The sidebar's nav is the
// sheet; the button toggles it, and choosing anything closes it.
(function () {
  var btn = document.getElementById("side-menu-btn");
  var nav = document.getElementById("side-nav");
  if (!btn || !nav) return;
  function setOpen(open) {
    document.body.classList.toggle("menu-open", open);
    btn.setAttribute("aria-expanded", open ? "true" : "false");
  }
  btn.addEventListener("click", function () {
    setOpen(!document.body.classList.contains("menu-open"));
  });
  nav.addEventListener("click", function (ev) {
    if (ev.target.closest("a, button")) setOpen(false);
  });
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape" && document.body.classList.contains("menu-open")) setOpen(false);
  });
})();
