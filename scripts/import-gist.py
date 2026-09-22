#!/usr/bin/env python3
"""Offline importer: extracts candidate endpoints, never trusts old 'working' flags.
Usage: import-gist.py downloaded-gist.md config.json
New endpoints are disabled and ineligible until explicitly reviewed by the user.
"""
import hashlib
import json
import re
import sys
from urllib.parse import urlsplit
from pathlib import Path

text = Path(sys.argv[1]).read_text()
path = Path(sys.argv[2])
config = json.loads(path.read_text())
known = {s['url'] for s in config['servers']}
count = 0
for url in re.findall(r'https://[^\s<>|)\]"`]+', text):
    parsed = urlsplit(url)
    if parsed.path not in ['/dns-query', '/query', '/uncensored', '/adblock', '/adultfilter', '/p0', '/p1', '/p2', '/p3', '/doh/family-filter/']:
        continue
    if any(x in parsed.netloc for x in ['example.', 'nextdns', 'blockerdns']):
        continue
    if url in known:
        continue
    known.add(url)
    config['servers'].append(dict(id='gist_'+hashlib.sha256(url.encode()).hexdigest()[:12], name=parsed.netloc+' (gist, не проверен)', protocol='doh', url=url, enabled=False, eligible=False))
    count += 1
if len(config['servers']) > 300:
    raise SystemExit('Too many endpoints; nothing written')
path.write_text(json.dumps(config, ensure_ascii=False, indent=2)+'\n')
print(f'Imported {count} disabled candidates; total {len(config["servers"])}')
