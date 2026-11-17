#!/usr/bin/env node
// floor-guard.mjs — diff-scoped enforcement of the CONSTRAINTS.md floor.
// Usage: node floor-guard.mjs [--base <ref>]   (default base: origin/main)
// Exit codes: 0 clean · 1 floor violation (block) · 2 could not run (never reads as clean).
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const base = (() => {
	const i = process.argv.indexOf("--base");
	return i > -1 ? process.argv[i + 1] : "origin/main";
})();

const git = (args, { diffExit = false } = {}) => {
	try {
		return execFileSync("git", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
	} catch (e) {
		return diffExit && e.status === 1 && typeof e.stdout === "string" ? e.stdout : null;
	}
};
const bail = (msg) => {
	console.error("floor-guard: " + msg);
	process.exit(2);
};

const top = git(["rev-parse", "--show-toplevel"])?.trim();
if (!top) bail("not inside a git work tree");
process.chdir(top);

const mergeBase = git(["merge-base", base, "HEAD"])?.trim();
if (!mergeBase) bail("no merge base against " + base);

const tracked = git(["diff", "--unified=0", mergeBase, "--"]);
if (tracked === null) bail("could not diff against " + mergeBase);
const untrackedFiles = git(["ls-files", "--others", "--exclude-standard"]);
if (untrackedFiles === null) bail("could not list untracked files");
const untracked = untrackedFiles
	.split("\n")
	.filter(Boolean)
	.map((f) => {
		const d = git(["diff", "--no-index", "--unified=0", "/dev/null", f], { diffExit: true });
		if (d === null) bail("could not diff untracked file " + f);
		return d;
	})
	.join("\n");
const diff = tracked + "\n" + untracked;

const added = [],
	removed = [],
	deleted = [];
const pathOf = (s) => s.replace(/^[ab]\//, "");
let file = "",
	oldFile = "",
	inHeader = false;
for (const line of diff.split("\n")) {
	if (line.startsWith("diff ")) inHeader = true;
	else if (line.startsWith("@@")) inHeader = false;
	else if (inHeader) {
		if (line.startsWith("--- ")) oldFile = pathOf(line.slice(4));
		else if (line.startsWith("+++ ")) {
			const newFile = pathOf(line.slice(4));
			file = newFile === "/dev/null" ? oldFile : newFile;
			if (newFile === "/dev/null") deleted.push(file);
		}
	} else if (line.startsWith("+")) added.push({ file, text: line.slice(1) });
	else if (line.startsWith("-")) removed.push({ file, text: line.slice(1) });
}

const findings = [];
const flag = (rule, f, text) => findings.push({ rule, file: f, text: text.trim().slice(0, 120) });
const isTest = (f) => /\.(test|spec)\.|_test\.|test_/.test(f);
const isConstraints = (f) => /CONSTRAINTS\.md$/.test(f);

// 1. Silenced checker. 4. Unfinished work. 2. Test made easier. 3. New exception row.
const SUPPRESSIONS = /@ts-ignore|@ts-nocheck|@ts-expect-error|eslint-disable|biome-ignore|# *noqa|# *type: *ignore|istanbul ignore|nosemgrep|gitleaks:allow|\/\/ *nolint/;
const STUBS = /throw new (Error|NotImplemented).*[Nn]ot implemented|catch\s*\(\w*\)\s*\{\s*\}|catch\s*\{\s*\}|\bTODO\b|\bpass\s*# *stub/;
const SKIPS = /\.(skip|todo)\b|\bxit\(|\bxdescribe\(|@pytest\.mark\.skip|t\.Skip\(/;

const IGNORE_FILE = ".constraintsignore";
const ignores = (() => {
	try {
		return readFileSync(IGNORE_FILE, "utf8")
			.split("\n")
			.map((l) => l.trim())
			.filter((l) => l && !l.startsWith("#"));
	} catch {
		return [];
	}
})();
const ignored = (f) => ignores.some((g) => new RegExp("^" + g.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*") + "$").test(f));


for (const { file, text } of added) {
	if (ignored(file)) continue;
	if (SUPPRESSIONS.test(text)) flag("silenced-checker", file, text);
	if (STUBS.test(text)) flag("unfinished-work", file, text);
	if (SKIPS.test(text)) flag("test-made-easier", file, text);
	if (isConstraints(file) && /^\| *(W|E)\d+ *\|/.test(text)) flag("new-exception", file, text);
}

for (const f of deleted) if (isTest(f)) flag("test-deleted", f, "file deleted");
for (const { file, text } of removed) {
	if (isTest(file) && !deleted.includes(file) && /\b(expect|assert|should)\b/.test(text)) {
		flag("assertion-removed", file, text);
	}
}

// A CONSTRAINTS.md rule weakened or removed.
const ruleKey = (t) => {
	const s = t.trim();
	if (s.startsWith("|")) return s.split("|").map((c) => c.trim()).filter(Boolean)[0] ?? "";
	if (/^[-*] /.test(s)) return s.slice(2).split(":")[0].trim();
	return null;
};
const isException = (t) => /^\| *(W|E)\d+ *\|/.test(t.trim());
const MIN_BEFORE = /(>=|>|≥|at least|minimum|\bmin\b|no less than|not fall|not drop)\s*$/;
const MAX_BEFORE = /(<=|<|≤|at most|maximum|\bmax\b|no more than|under|below|not grow|not exceed)\s*$/;
const MIN_AFTER = /^\s*\S*\s*(?:\|\s*)?(or more|or higher|must not fall|must not drop)/;
const MAX_AFTER = /^\s*\S*\s*(?:\|\s*)?(or less|or lower|must not grow|must not exceed)/;
const thresholds = (t) => {
	const out = [],
		re = /\d+(?:\.\d+)?/g;
	let m;
	while ((m = re.exec(t))) {
		const before = t.slice(Math.max(0, m.index - 24), m.index).toLowerCase();
		const after = t.slice(m.index + m[0].length, m.index + m[0].length + 40).toLowerCase();
		const dir = MIN_BEFORE.test(before) || MIN_AFTER.test(after) ? "min"
			: MAX_BEFORE.test(before) || MAX_AFTER.test(after) ? "max" : null;
		out.push({ n: Number(m[0]), dir });
	}
	return out;
};
const removedRules = removed.filter((l) => isConstraints(l.file) && ruleKey(l.text) !== null);
const addedRules = added.filter((l) => isConstraints(l.file) && ruleKey(l.text) !== null);
for (const r of removedRules) {
	const a = addedRules.find((x) => ruleKey(x.text) === ruleKey(r.text));
	if (!a) {
		if (!isException(r.text)) flag("rule-removed", r.file, r.text);
		continue;
	}
	const before = thresholds(r.text),
		after = thresholds(a.text);
	let verdict = null;
	for (const dir of ["min", "max", null]) {
		const was = before.filter((x) => x.dir === dir),
			now = after.filter((x) => x.dir === dir);
		was.forEach((b, i) => {
			const n = now[i];
			if (verdict) return;
			if (!n) verdict = "threshold-removed";
			else if (n.n === b.n) return;
			else if (dir === "min" ? n.n < b.n : dir === "max" ? n.n > b.n : true) {
				verdict = dir ? "threshold-loosened" : "threshold-changed";
			}
		});
	}
	if (verdict) flag(verdict, r.file, r.text + "  ->  " + a.text);
}

if (findings.length === 0) {
	console.log("floor-guard: clean");
	process.exit(0);
}
console.error("floor-guard: " + findings.length + " floor violation(s):");
for (const f of findings) console.error(`  [${f.rule}] ${f.file}: ${f.text}`);
console.error(
	"\nEach is a move that lowers the bar. Fix the code, or route it through a tracked exception.",
);
process.exit(1);
