from http.server import BaseHTTPRequestHandler, HTTPServer
import os


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok\n")
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(f"opsi-e2e {os.environ.get('OPSI_E2E_MARKER', 'ready')}\n".encode())


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
