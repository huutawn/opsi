from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(500 if self.path == "/health" else 200)
        self.end_headers()
        self.wfile.write(b"bad\n")


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
