# Passes only Andaru's signature policy for namespace $ns: reject by default; under $ns exactly two docker scopes,
# $ns requiring the release key and $ns/andaru-nightly the nightly key; no other scope anywhere requiring a signature.
def signed($key): . == [{"type": "sigstoreSigned", "keyPath": $key, "signedIdentity": {"type": "matchRepository"}}];
.default == [{"type": "reject"}]
and ([.transports.docker | keys[] | select(. == $ns or startswith($ns + "/"))] | sort) == ([$ns, $ns + "/andaru-nightly"] | sort)
and (.transports.docker[$ns] | signed("/etc/pki/containers/andaru.pub"))
and (.transports.docker[$ns + "/andaru-nightly"] | signed("/etc/pki/containers/andaru-nightly.pub"))
and ([.transports[][] | .[] | select(.type == "sigstoreSigned")] | length == 2)
