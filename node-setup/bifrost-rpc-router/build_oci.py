"""Deterministic scratch OCI archive: the router binary and a CA bundle, nothing else."""
import gzip, hashlib, io, json, sys, tarfile
from pathlib import Path
root = Path(__file__).resolve().parent; build = Path(sys.argv[1]); name = sys.argv[2]
sha = lambda b: hashlib.sha256(b).hexdigest()
layer = io.BytesIO()
with tarfile.open(fileobj=layer, mode='w', format=tarfile.PAX_FORMAT) as t:
    def add(path, data=None, mode=0o755):
        i = tarfile.TarInfo(path); i.mtime = 0; i.uid = i.gid = 0; i.uname = i.gname = ''; i.mode = mode
        if data is None: i.type = tarfile.DIRTYPE; t.addfile(i)
        else: i.size = len(data); t.addfile(i, io.BytesIO(data))
    add('bifrost-rpc-router', (build / 'bifrost-rpc-router').read_bytes(), 0o755)
    for d in ('etc', 'etc/ssl', 'etc/ssl/certs'): add(d)
    add('etc/ssl/certs/ca-certificates.crt', (build / 'ca-certificates.crt').read_bytes(), 0o644)
raw = layer.getvalue(); gz = gzip.compress(raw, mtime=0)
cfg = json.dumps({'architecture': 'amd64', 'os': 'linux', 'created': '1970-01-01T00:00:00Z',
    'config': {'User': '10001:10001', 'Env': ['GOMEMLIMIT=256MiB'], 'Entrypoint': ['/bifrost-rpc-router']},
    'rootfs': {'type': 'layers', 'diff_ids': ['sha256:' + sha(raw)]}, 'history': [{'created': '1970-01-01T00:00:00Z', 'created_by': 'build_oci.py'}]}).encode()
man = json.dumps({'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.manifest.v1+json',
    'config': {'mediaType': 'application/vnd.oci.image.config.v1+json', 'digest': 'sha256:' + sha(cfg), 'size': len(cfg)},
    'layers': [{'mediaType': 'application/vnd.oci.image.layer.v1.tar+gzip', 'digest': 'sha256:' + sha(gz), 'size': len(gz)}]}).encode()
index = json.dumps({'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.index.v1+json', 'manifests': [{'mediaType': 'application/vnd.oci.image.manifest.v1+json',
    'digest': 'sha256:' + sha(man), 'size': len(man), 'platform': {'architecture': 'amd64', 'os': 'linux'},
    'annotations': {'io.containerd.image.name': name, 'org.opencontainers.image.ref.name': name.rsplit(':', 1)[1]}}]}).encode()
with tarfile.open(root / 'proxy-image.tar', 'w') as out:
    def put(path, data):
        i = tarfile.TarInfo(path); i.size = len(data); i.mtime = 0; i.mode = 0o644; out.addfile(i, io.BytesIO(data))
    put('oci-layout', b'{"imageLayoutVersion":"1.0.0"}'); put('index.json', index)
    for b in (cfg, man, gz): put('blobs/sha256/' + sha(b), b)
print('manifest sha256:' + sha(man))
