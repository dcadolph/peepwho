// Taking things with you.
//
// Every answer peepwho gives is on its way somewhere else: pasted into a ticket,
// dropped in a channel, attached to a review, carried into a meeting. A value
// you can read on screen and cannot take with you is half an answer, so the
// affordance for leaving with it belongs next to the thing itself rather than
// in a menu somewhere.
//
// This lives apart from any one page because both the app and the org chart
// need it, and two copies would drift.

// el builds an element in one call, since nearly every one of these needs a
// class and some text and nothing else.
function el(tag, cls, text) {
	const node = document.createElement(tag);
	if (cls) node.className = cls;
	if (text != null) node.textContent = text;
	return node;
}

// copyButton copies a value to the clipboard.
//
// text may be a string or a function returning one, so a button can capture
// what is on screen at the moment it is pressed rather than when it was built.
function copyButton(text, label) {
	const idle = label || "copy";
	const button = el("button", "copy", idle);
	button.type = "button";
	button.title = "Copy to the clipboard";
	button.addEventListener("click", async (ev) => {
		// Rows are clickable; copying is not the same as opening one.
		ev.stopPropagation();
		const value = typeof text === "function" ? text() : text;
		try {
			await navigator.clipboard.writeText(value);
			button.textContent = "copied";
		} catch (err) {
			// Clipboard access is refused outside a secure context, which is the
			// normal case for a plain http:// instance on an internal network.
			button.textContent = "select it";
			window.prompt("Copy this:", value);
		}
		setTimeout(() => (button.textContent = idle), 1200);
	});
	return button;
}

// csvCell quotes a value for a spreadsheet, doubling any quotes inside it. A
// string starting with = + - @ or a stray tab or carriage return would be run
// as a formula the moment the export opens, and names come from sources third
// parties control, so such a cell is prefixed with a quote to stay text.
function csvCell(v) {
	let s = String(v === undefined || v === null ? "" : v);
	if (typeof v === "string" && /^[=+\-@\t\r]/.test(s)) s = "'" + s;
	return '"' + s.replace(/"/g, '""') + '"';
}

// downloadCSV hands the reader a file of exactly what they are looking at,
// filtered and sorted as they left it, so the export matches the screen rather
// than dumping the whole index back at them.
function downloadCSV(name, header, rows) {
	const lines = [header.map(csvCell).join(",")];
	for (const row of rows) lines.push(row.map(csvCell).join(","));
	const blob = new Blob([lines.join("\n")], { type: "text/csv;charset=utf-8" });
	const a = document.createElement("a");
	a.href = URL.createObjectURL(blob);
	a.download = name;
	document.body.appendChild(a);
	a.click();
	document.body.removeChild(a);
	URL.revokeObjectURL(a.href);
}

// exportButton downloads whatever the caller decides at the moment of the click.
function exportButton(label, build) {
	const button = el("button", "copy", label);
	button.type = "button";
	button.title = "Download what is shown, as a spreadsheet";
	button.addEventListener("click", (ev) => {
		ev.stopPropagation();
		build();
	});
	return button;
}
