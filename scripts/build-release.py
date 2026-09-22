#!/usr/bin/env python3
"""Build a static binary and native APK v3/IPK without a target C compiler.
APK builder must come from the matching OpenWrt SDK, NOT Android/Alpine apk.
Usage: build-release.py --arch arm64 --openwrt-arch aarch64_cortex-a53 --apk /path/to/sdk/staging_dir/host/bin/apk
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

p=argparse.ArgumentParser()
p.add_argument('--arch', required=True, choices=['arm64','arm','mips','mipsle','amd64'])
p.add_argument('--openwrt-arch', required=True)
p.add_argument('--apk')
p.add_argument('--go', default='go')
a=p.parse_args()
root=Path(__file__).resolve().parents[1]
out=root/'dist';out.mkdir(exist_ok=True)
version=(root/'VERSION').read_text().strip()+'-r'+(root/'RELEASE').read_text().strip()
env=dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=a.arch, GOMIPS='softfloat', GOARM='7', GOCACHE=os.environ.get('GOCACHE','/tmp/dns-go-cache'))
binary=out/f'dns-scout-{a.arch}'
subprocess.run([a.go,'build','-buildvcs=false','-trimpath','-ldflags=-s -w -X main.version='+version,'-o',str(binary),'./cmd/dns-scout'],cwd=root,env=env,check=True)
with tempfile.TemporaryDirectory(prefix='dns-scout-package-') as tmp:
    stage=Path(tmp)/'data';shutil.copytree(root/'files',stage)
    (stage/'usr/sbin').mkdir(parents=True,exist_ok=True)
    shutil.copy2(binary,stage/'usr/sbin/dns-scout')
    for path in stage.rglob('*'):
        path.chmod(0o755 if path.is_dir() or path.name=='dns-scout' and path.parent.name in ['sbin','rpcd','init.d'] else 0o644)
    (stage/'etc/dns-scout/config.json').chmod(0o600)
    (stage/'etc/uci-defaults').mkdir(parents=True,exist_ok=True)
    # Read once on install/first boot; package hooks handle installed systems.
    (stage/'etc/uci-defaults/90-dns-scout').write_text('#!/bin/sh\n/etc/init.d/dns-scout enable\nexit 0\n')
    (stage/'etc/uci-defaults/90-dns-scout').chmod(0o755)
    def archive(directory, target):
        with tarfile.open(target,'w:gz',format=tarfile.GNU_FORMAT) as tar:
            for file in sorted(directory.rglob('*')):
                info=tar.gettarinfo(str(file),'./'+str(file.relative_to(directory)))
                info.uid=info.gid=0;info.uname=info.gname='root';info.mtime=0
                with file.open('rb') if file.is_file() else io.BytesIO() as data:
                    tar.addfile(info,data if file.is_file() else None)
    data=Path(tmp)/'data.tar.gz';archive(stage,data)
    control=Path(tmp)/'control';control.mkdir()
    (control/'control').write_text(f'Package: luci-app-dns-scout\nVersion: {version}\nArchitecture: {a.openwrt_arch}\nMaintainer: DNS Scout contributors\nSection: luci\nPriority: optional\nLicense: MIT\nDepends: luci-base, rpcd, ca-bundle, https-dns-proxy\nDescription: DoH benchmarking and verified selection for LuCI\n')
    (control/'conffiles').write_text('/etc/dns-scout/config.json\n')
    for script in ['postinst','prerm']:
        shutil.copy2(root/'scripts'/script,control/script)
    archive(control,Path(tmp)/'control.tar.gz')
    (Path(tmp)/'debian-binary').write_text('2.0\n')
    with tarfile.open(out/f'luci-app-dns-scout_{version}_{a.openwrt_arch}.ipk','w:gz',format=tarfile.GNU_FORMAT) as tar:
        for name in ['debian-binary','control.tar.gz','data.tar.gz']:
            info=tar.gettarinfo(str(Path(tmp)/name),'./'+name);info.uid=info.gid=0;info.uname=info.gname='root';info.mtime=0
            with (Path(tmp)/name).open('rb') as f:tar.addfile(info,f)
    if a.apk:
        metadata=stage/'lib/apk/packages'
        metadata.mkdir(parents=True,exist_ok=True)
        (metadata/'luci-app-dns-scout.conffiles').write_text('/etc/dns-scout/config.json\n')
        digest=hashlib.sha256((stage/'etc/dns-scout/config.json').read_bytes()).hexdigest()
        (metadata/'luci-app-dns-scout.conffiles_static').write_text('/etc/dns-scout/config.json '+digest+'\n')
        # Run this script under SDK fakeroot for root ownership in APK metadata.
        for path in [stage,*stage.rglob('*')]:
            os.chown(path,0,0)
        subprocess.run([a.apk,'mkpkg','--info','name:luci-app-dns-scout','--info',f'version:{version}','--info',f'arch:{a.openwrt_arch}','--info','description:DoH benchmarking and verified selection for LuCI','--info','license:MIT','--info','depends:luci-base rpcd ca-bundle https-dns-proxy','--files',str(stage),'--script','post-install:'+str(root/'scripts/postinst'),'--script','post-upgrade:'+str(root/'scripts/postinst'),'--script','pre-deinstall:'+str(root/'scripts/prerm'),'--output',str(out/f'luci-app-dns-scout-{version}.{a.openwrt_arch}.apk')],check=True)
(out/'release.json').write_text(json.dumps({"version":version},indent=2)+'\n')
checks=[]
for file in sorted(out.iterdir()):
    if file.name=='release.json' or (file.suffix in ('.apk','.ipk') and version in file.name):
        checks.append(hashlib.sha256(file.read_bytes()).hexdigest()+'  '+file.name)
(out/'SHA256SUMS').write_text('\n'.join(checks)+'\n')
print('Built',version,a.openwrt_arch)
