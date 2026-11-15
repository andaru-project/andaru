#!/bin/sh
# Exercises the installed portable-backup surface against the packaged engine's normal root-local socket.
# Runs only inside install-test's disposable Debian 13 systemd container.
set -eu

fail() {
	echo "backup-check: $*" >&2
	exit 1
}

work=$(mktemp -d /var/tmp/andaru-backup-check.XXXXXX)
chmod 0700 "$work"
success=0
cleanup() {
	if [ "$success" -eq 1 ]; then
		rm -rf -- "$work"
	else
		echo "backup-check: failure artifacts preserved in $work" >&2
	fi
}
trap cleanup EXIT
passphrase=TEST_ONLY_BACKUP_PASSPHRASE
secret=TEST_ONLY_SECRET_VALUE
package_version=$(dpkg-query -W -f='${Version}' andaru)

# Run a command on a real controlling terminal and answer only the expected prompts.
cat >"$work/pty-run.py" <<'PY'
import errno, json, os, pty, select, signal, sys, termios, time

output, answers, command = sys.argv[1], json.loads(sys.argv[2]), sys.argv[3:]
age_wait_noecho = os.environ.get("ANDARU_AGE_WAIT_NOECHO") == "1"
pid, fd = pty.fork()
if pid == 0:
    os.execvp(command[0], command)
seen = b""
next_answer = 0
deadline = time.monotonic() + 180
with open(output, "wb") as log:
    while True:
        if time.monotonic() >= deadline:
            os.kill(pid, signal.SIGKILL)
            raise SystemExit("PTY command timed out")
        readable, _, _ = select.select([fd], [], [], 0.2)
        if readable:
            try:
                data = os.read(fd, 4096)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not data:
                break
            log.write(data)
            log.flush()
            seen += data
        while next_answer < len(answers) and answers[next_answer][0].encode() in seen:
            # age v1.2.1 prints before ReadPassword; only this independent tool waits, while product replies stay immediate to catch echo-order regressions.
            if age_wait_noecho and termios.tcgetattr(fd)[3] & termios.ECHO:
                break
            os.write(fd, answers[next_answer][1].encode() + b"\n")
            next_answer += 1
            seen = b""
_, status = os.waitpid(pid, 0)
if next_answer != len(answers):
    raise SystemExit("expected PTY prompt was not seen")
raise SystemExit(os.waitstatus_to_exitcode(status))
PY
chmod 0700 "$work/pty-run.py"
pty_run() {
	output=$1
	answers=$2
	shift 2
	python3 "$work/pty-run.py" "$output" "$answers" "$@"
}
pty_run_age() {
	output=$1
	answers=$2
	shift 2
	ANDARU_AGE_WAIT_NOECHO=1 python3 "$work/pty-run.py" "$output" "$answers" "$@"
}

snapshot_host() {
	name=$1
	if [ -f /var/lib/andaru/generations/pointers.json ]; then
		cp /var/lib/andaru/generations/pointers.json "$work/pointers-$name.json"
	else
		: >"$work/pointers-$name.json"
	fi
	for unit in systemd-networkd.service nftables.service kea-dhcp4-server.service kea-dhcp6-server.service \
		unbound.service andaru-network-boot.service; do
		printf '%s %s\n' "$unit" "$(systemctl show -P InvocationID "$unit")"
	done >"$work/invocations-$name"
	nft -s -j list ruleset >"$work/rules-$name.json"
}

systemctl is-active --quiet andaru-engine.service || fail "packaged engine is not active"
andaru ctl config show >"$work/source-before.json"
snapshot_host before

cat >"$work/fixture.json" <<EOF
{"operations":[
 {"op":"create","collection":"links","localId":"parent","value":{"name":"fixture-parent","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"02:00:00:00:00:99"}},
 {"op":"create","collection":"links","localId":"ppp","value":{"name":"fixture-ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":"@parent","username":"fixture-user","password":{"secret":"PORTABLE_BACKUP_TEST_PASSWORD","set":"$secret","expectedVersion":"0"}}}
]}
EOF
andaru ctl config changes <"$work/fixture.json" >"$work/fixture-result.json"
andaru ctl config show >"$work/source.json"

pty_run "$work/plain-create.log" '[]' andaru ctl backup create --output "$work/plain.zip"
pty_run "$work/secret-create.log" \
	'[["New backup passphrase: ","TEST_ONLY_BACKUP_PASSPHRASE"],["Repeat backup passphrase: ","TEST_ONLY_BACKUP_PASSPHRASE"]]' \
	andaru ctl backup create --output "$work/secret.age" --include-secrets

andaru ctl backup inspect --input "$work/plain.zip" >"$work/plain-inspect.json"
pty_run "$work/secret-inspect.log" \
	'[["Backup passphrase: ","TEST_ONLY_BACKUP_PASSPHRASE"]]' \
	andaru ctl backup inspect --input "$work/secret.age"

# The archive consumer is deliberately independent of Andaru: age decrypts, then Python's ZIP reader validates members.
pty_run_age "$work/age.log" \
	'[["Enter passphrase", "TEST_ONLY_BACKUP_PASSPHRASE"]]' \
	age --decrypt -o "$work/secret.zip" "$work/secret.age"
PACKAGE_VERSION=$package_version WORK=$work SECRET=$secret PASSPHRASE=$passphrase python3 <<'PY'
import hashlib, json, os, zipfile
from pathlib import Path

root = Path(os.environ["WORK"])
secret = os.environ["SECRET"].encode()
passphrase = os.environ["PASSPHRASE"].encode()
package_version = os.environ["PACKAGE_VERSION"]
source = json.loads((root / "source.json").read_text())
source_before = json.loads((root / "source-before.json").read_text())
pointers_raw = (root / "pointers-before.json").read_text()
pointers = json.loads(pointers_raw) if pointers_raw else {"live": None, "pending": None}

manifests = []
for archive, expected_names, expected_scopes in [
    ("plain.zip", {"manifest.json", "config.json"}, ["configuration"]),
    ("secret.zip", {"manifest.json", "config.json", "configuration-secrets.json"}, ["configuration", "configurationSecrets"]),
]:
    with zipfile.ZipFile(root / archive) as zf:
        if set(zf.namelist()) != expected_names:
            raise SystemExit(f"{archive}: unexpected ZIP members {zf.namelist()}")
        bodies = {name: zf.read(name) for name in zf.namelist()}
    manifest = json.loads(bodies["manifest.json"])
    manifests.append(manifest)
    if manifest["andaruVersion"] != package_version:
        raise SystemExit(f"{archive}: build label {manifest['andaruVersion']!r} != installed package {package_version!r}")
    if manifest["savedVersion"] != source["savedVersion"] or source_before["savedVersion"] == source["savedVersion"]:
        raise SystemExit(f"{archive}: wrong saved source version")
    if manifest["scopes"] != expected_scopes:
        raise SystemExit(f"{archive}: wrong scopes")
    if manifest["network"]["live"] != pointers.get("live") or manifest["network"]["pending"] != pointers.get("pending"):
        raise SystemExit(f"{archive}: live/pending source pointers disagree")
    if pointers.get("live") is None and manifest["network"]["liveSavedVersion"] != "":
        raise SystemExit(f"{archive}: invented a live saved version")
    for name, payload in manifest["payloads"].items():
        body = bodies[name]
        if payload["size"] != len(body) or payload["sha256"] != hashlib.sha256(body).hexdigest():
            raise SystemExit(f"{archive}: payload metadata mismatch for {name}")
    if json.loads(bodies["config.json"])["schemaVersion"] != 1:
        raise SystemExit(f"{archive}: initial schema was not preserved")
    if archive == "plain.zip" and (secret in b"".join(bodies.values()) or passphrase in b"".join(bodies.values()) or "configurationSecrets" in manifest["scopes"]):
        raise SystemExit("default archive exposed the TEST_ONLY secret")
    if archive == "secret.zip" and secret not in bodies["configuration-secrets.json"]:
        raise SystemExit("independently decrypted archive lost the TEST_ONLY secret")
if manifests[0]["savedVersion"] != manifests[1]["savedVersion"]:
    raise SystemExit("plain and encrypted archives captured different saved heads")
PY

# Move the destination head, then restore only configuration. Its same-named local secret must not bind implicitly.
printf '%s\n' '{"name":"post-backup-mutation"}' | andaru ctl config create zones >"$work/mutation.json"
pty_run "$work/config-restore.log" \
	'[["Type RESTORE to replace the selected saved stores (networking is not applied): ","RESTORE"]]' \
	andaru ctl backup restore --input "$work/plain.zip" --stores configuration
andaru ctl config show >"$work/after-config-restore.json"
andaru ctl config list links >"$work/after-config-links.json"
if andaru ctl preview network >"$work/unresolved-preview.out" 2>"$work/unresolved-preview.err"; then
	fail "network preview accepted an unresolved secret"
fi

WORK=$work python3 <<'PY'
import json, os
from pathlib import Path

root = Path(os.environ["WORK"])
def objects(path):
    text = path.read_text(errors="strict").replace("\r", "")
    decoder, result, at = json.JSONDecoder(), [], 0
    while True:
        at = text.find("{", at)
        if at < 0:
            return result
        try:
            value, end = decoder.raw_decode(text, at)
        except json.JSONDecodeError:
            at += 1
            continue
        result.append(value)
        at = end

rows = objects(root / "config-restore.log")
preview = next((row for row in rows if row.get("state") == "restore-preview"), None)
result = next((row for row in rows if row.get("state") == "saved"), None)
if (not preview or "PORTABLE_BACKUP_TEST_PASSWORD" not in preview.get("unresolvedSecrets", []) or not result
        or result.get("savedVersion") == preview.get("expectedSavedVersion")):
    raise SystemExit("configuration restore did not preview and record a new version with the unresolved secret")
state = json.loads((root / "after-config-restore.json").read_text())
if state["savedVersion"] != result["savedVersion"] or state["value"]["schemaVersion"] != 2:
    raise SystemExit("configuration restore did not publish the expected schema-2 saved head")
ppp = next(link for link in state["value"]["links"] if link["name"] == "fixture-ppp")
if ppp["password"].get("unresolved") is not True or ppp["password"].get("isSet") is not False:
    raise SystemExit("same-named destination secret was silently rebound")
if (root / "unresolved-preview.out").stat().st_size:
    raise SystemExit("failed unresolved preview wrote success output")
error = json.loads((root / "unresolved-preview.err").read_text())["error"]
if error.get("code") != "VALIDATION_FAILED" or not any(
        detail.get("path", "").endswith("/password")
        and "unresolved; explicitly bind or set it before applying" in detail.get("message", "")
        for detail in error.get("details", [])):
    raise SystemExit("network preview did not return the actionable unresolved-secret validation error")

listed = json.loads((root / "after-config-links.json").read_text())
row = next(item for item in listed["items"] if item["value"]["name"] == "fixture-ppp")
replacement = dict(row["value"])
link_id = replacement.pop("id")
replacement["password"] = {"secret": "PORTABLE_BACKUP_TEST_PASSWORD"}
(root / "repair.json").write_text(json.dumps(replacement, separators=(",", ":")))
(root / "repair-meta").write_text(f"{link_id}\n{row['version']}\n")
PY

link_id=$(sed -n '1p' "$work/repair-meta")
link_version=$(sed -n '2p' "$work/repair-meta")
andaru ctl config replace --version "$link_version" links "$link_id" <"$work/repair.json" >"$work/repair-result.json"
andaru ctl config show >"$work/repaired.json"
andaru ctl preview network >"$work/repaired-preview.json"
andaru ctl backup create --output "$work/before-secrets.zip" >"$work/before-secrets-create.json"

# A secrets-only restore is separately previewed and committed; it must preserve the schema-2 declaration.
pty_run "$work/secrets-restore.log" \
	'[["Backup passphrase: ","TEST_ONLY_BACKUP_PASSPHRASE"],["Type RESTORE to replace the selected saved stores (networking is not applied): ","RESTORE"]]' \
	andaru ctl backup restore --input "$work/secret.age" --stores configurationSecrets
andaru ctl config show >"$work/final.json"
andaru ctl backup create --output "$work/after-secrets.zip" >"$work/after-secrets-create.json"
snapshot_host after

WORK=$work python3 <<'PY'
import json, os, zipfile
from pathlib import Path

root = Path(os.environ["WORK"])
repaired = json.loads((root / "repaired.json").read_text())
ppp = next(link for link in repaired["value"]["links"] if link["name"] == "fixture-ppp")
if repaired["value"]["schemaVersion"] != 2 or ppp["password"].get("unresolved", False) or not ppp["password"].get("isSet"):
    raise SystemExit("explicit secret binding did not repair the schema-2 declaration")
preview = json.loads((root / "repaired-preview.json").read_text())
if "SECRET_UNRESOLVED" in {finding["code"] for finding in preview["findings"]}:
    raise SystemExit("repaired reference remained blocked")

text = (root / "secrets-restore.log").read_text().replace("\r", "")
decoder, rows, at = json.JSONDecoder(), [], 0
while True:
    at = text.find("{", at)
    if at < 0:
        break
    try:
        value, end = decoder.raw_decode(text, at)
    except json.JSONDecodeError:
        at += 1
        continue
    rows.append(value)
    at = end
preview = next((row for row in rows if row.get("state") == "restore-preview"), None)
result = next((row for row in rows if row.get("state") == "saved"), None)
final = json.loads((root / "final.json").read_text())
if (not preview or not result or final["savedVersion"] != result["savedVersion"]
        or result.get("savedVersion") == preview.get("expectedSavedVersion")):
    raise SystemExit("secrets-only restore did not preview and publish a new saved version")
with zipfile.ZipFile(root / "before-secrets.zip") as before_zip, zipfile.ZipFile(root / "after-secrets.zip") as after_zip:
    before_config = before_zip.read("config.json")
    after_config = after_zip.read("config.json")
if before_config != after_config or json.loads(after_config)["schemaVersion"] != 2:
    raise SystemExit("secrets-only restore changed the canonical schema-2 configuration document")
PY

cmp "$work/pointers-before.json" "$work/pointers-after.json" >/dev/null || fail "saved-only backup operations changed live generation pointers"
cmp "$work/invocations-before" "$work/invocations-after" >/dev/null || fail "saved-only backup operations restarted a network service"
cmp "$work/rules-before.json" "$work/rules-after.json" >/dev/null || fail "saved-only backup operations changed firewall rules"
for log in "$work"/*.log; do
	if grep -Fq "$passphrase" "$log" || grep -Fq "$secret" "$log"; then
		fail "terminal output exposed a TEST_ONLY fixture secret in $(basename -- "$log")"
	fi
done

echo "backup-check: plain/encrypted package backup, independent interoperability and saved-only restore passed"
success=1
