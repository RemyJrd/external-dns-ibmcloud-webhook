import json
import re
import sys
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

INSTANCE = "local-instance"
ZONES = [{"id": "z-lan", "name": "estreem.lan"}, {"id": "z-io", "name": "sit.estreem.io"}]
RECORDS = {
    "z-lan": [
        {"id": "seed-contract", "name": "auth.int.estreem.lan", "type": "CNAME", "ttl": 43200,
         "rdata": {"cname": "aasc-alb.int.estreem.lan"}},
        {"id": "seed-route", "name": "aasc.int.estreem.lan", "type": "CNAME", "ttl": 43200,
         "rdata": {"cname": "npr01-ese01-ingress.containers.appdomain.cloud"}},
    ],
    "z-io": [],
}
LOG = []
RDATA_KEY = {"A": "ip", "CNAME": "cname", "TXT": "text"}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        sys.stderr.write("%s %s\n" % (self.command, self.path))

    def _json(self, code, body=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        if self.headers.get("Content-Type", "").startswith("application/x-www-form-urlencoded"):
            return parse_qs(raw.decode())
        return json.loads(raw or b"{}")

    def _auth_ok(self):
        if self.headers.get("Authorization") != "Bearer fake-token":
            self._json(401, {"errors": [{"message": "unauthorized"}]})
            return False
        return True

    def do_POST(self):
        u = urlparse(self.path)
        if u.path == "/identity/token":
            form = self._body()
            if form.get("apikey", [""])[0] != "fake-apikey":
                return self._json(400, {"errorMessage": "bad apikey"})
            now = int(time.time())
            return self._json(200, {"access_token": "fake-token", "refresh_token": "r", "token_type": "Bearer",
                                    "expires_in": 3600, "expiration": now + 3600})
        m = re.fullmatch(r"/v1/instances/([^/]+)/dnszones/([^/]+)/resource_records", u.path)
        if m and self._auth_ok():
            zone = m.group(2)
            b = self._body()
            name = b["name"]
            fqdn = name
            for r in RECORDS[zone]:
                if r["name"] == fqdn and r["type"] == b["type"] and r["rdata"] == b["rdata"]:
                    return self._json(409, {"errors": [{"message": "duplicate record"}]})
                if r["name"] == fqdn and ("CNAME" in (r["type"], b["type"])):
                    return self._json(409, {"errors": [{"message": "CNAME conflict"}]})
            rec = {"id": str(uuid.uuid4())[:8], "name": fqdn, "type": b["type"], "ttl": b.get("ttl", 900), "rdata": b["rdata"]}
            RECORDS[zone].append(rec)
            LOG.append(["create", rec["type"], rec["name"], list(rec["rdata"].values())[0], rec["ttl"]])
            return self._json(200, rec)
        if not m:
            self._json(404, {})

    def do_PUT(self):
        u = urlparse(self.path)
        m = re.fullmatch(r"/v1/instances/([^/]+)/dnszones/([^/]+)/resource_records/([^/]+)", u.path)
        if not m:
            return self._json(404, {})
        if not self._auth_ok():
            return
        b = self._body()
        for r in RECORDS[m.group(2)]:
            if r["id"] == m.group(3):
                r.update({k: v for k, v in b.items() if k in ("ttl", "rdata", "name")})
                LOG.append(["update", r["type"], r["name"], list(r["rdata"].values())[0], r["ttl"]])
                return self._json(200, r)
        self._json(404, {})

    def do_DELETE(self):
        u = urlparse(self.path)
        m = re.fullmatch(r"/v1/instances/([^/]+)/dnszones/([^/]+)/resource_records/([^/]+)", u.path)
        if not m:
            return self._json(404, {})
        if not self._auth_ok():
            return
        zone = m.group(2)
        for r in list(RECORDS[zone]):
            if r["id"] == m.group(3):
                RECORDS[zone].remove(r)
                LOG.append(["delete", r["type"], r["name"], list(r["rdata"].values())[0], r["ttl"]])
                return self._json(204)
        self._json(404, {})

    def do_GET(self):
        u = urlparse(self.path)
        q = parse_qs(u.query)
        off, lim = int(q.get("offset", ["0"])[0]), int(q.get("limit", ["200"])[0])
        if u.path == "/_state":
            return self._json(200, RECORDS)
        if u.path == "/_log":
            return self._json(200, LOG)
        if u.path == "/healthz":
            return self._json(200, {})
        if not self._auth_ok():
            return
        if u.path == f"/v1/instances/{INSTANCE}/dnszones":
            page = ZONES[off:off + lim]
            return self._json(200, {"dnszones": page, "offset": off, "limit": lim, "count": len(page), "total_count": len(ZONES)})
        m = re.fullmatch(r"/v1/instances/([^/]+)/dnszones/([^/]+)/resource_records", u.path)
        if m and m.group(1) == INSTANCE and m.group(2) in RECORDS:
            recs = RECORDS[m.group(2)]
            page = recs[off:off + lim]
            return self._json(200, {"resource_records": page, "offset": off, "limit": lim, "count": len(page), "total_count": len(recs)})
        self._json(404, {})


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1]) if len(sys.argv) > 1 else 9999), Handler).serve_forever()
