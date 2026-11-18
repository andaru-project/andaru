#!/bin/sh
# npm audit at high severity, except the advisories listed in $allowed. Run it from web/.
#
# shortcut: GHSA-fc5q-39xc-vjpf (picomatch ReDoS) has no fixed release (<= 4.0.7 is affected) and
# reaches only the build and test tools, not the shipped assets. Remove it once a fixed picomatch
# is in web/package-lock.json; the audit warns when it no longer applies.
set -eu

allowed='https://github.com/advisories/GHSA-fc5q-39xc-vjpf'

# npm audit exits non-zero whenever it finds anything; the filter below decides.
report=$(npm audit --json) || true
# shellcheck disable=SC2016 # JavaScript, not shell: the template literals are for node.
printf '%s' "$report" | ALLOWED="$allowed" node -e '
const audit = JSON.parse(require("fs").readFileSync(0, "utf8"));
if (audit.error || !audit.vulnerabilities) {
	console.error("npm audit failed:", JSON.stringify(audit.error ?? audit));
	process.exit(1);
}
const allowed = new Set(process.env.ALLOWED.split(/\s+/).filter(Boolean));
const found = new Map();
for (const vulnerability of Object.values(audit.vulnerabilities)) {
	for (const via of vulnerability.via) {
		if (typeof via === "object" && (via.severity === "high" || via.severity === "critical")) {
			found.set(via.url, `${via.severity} ${via.name}: ${via.title}`);
		}
	}
}
let failed = false;
for (const [url, what] of found) {
	if (allowed.has(url)) {
		console.log(`allowed: ${what} ${url}`);
	} else {
		console.error(`${what} ${url}`);
		failed = true;
	}
}
for (const url of allowed) {
	if (!found.has(url)) console.log(`::warning::${url} no longer applies; remove it from scripts/ci/npm-audit.sh`);
}
process.exit(failed ? 1 : 0);
'
