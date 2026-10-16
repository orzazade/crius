"""Converts gitleaks' config/gitleaks.toml to the JSON that internal/redact embeds (step 4b).

Usage: gitleaks-rules-json.py <tag> <commit> <gitleaks.toml> > gitleaks.json. Needs Python 3.11+ (tomllib).
Keeps the fields redact uses (id, regex, secretGroup, entropy, keywords). Drops rules with a path (they match file
names, and redacted text has none) and rules with no regex. Allowlists are dropped: they only stop a match from
being reported, and redact prefers over-redacting to printing a secret (Crius 23:40).
"""
import hashlib
import json
import sys
import tomllib

tag, commit, path = sys.argv[1:4]
raw = open(path, "rb").read()
rules = [{k: r[k] for k in ("id", "regex", "secretGroup", "entropy", "keywords") if k in r}
         for r in tomllib.loads(raw.decode())["rules"] if "regex" in r and "path" not in r]
json.dump({"source": "https://github.com/gitleaks/gitleaks", "version": tag, "commit": commit,
           "toml_sha256": hashlib.sha256(raw).hexdigest(), "license": "MIT, see LICENSE.gitleaks",
           "rules": rules}, sys.stdout, indent=1)
print()
