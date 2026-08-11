from http.server import BaseHTTPRequestHandler, HTTPServer
import os


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"python\n")


HTTPServer(("", int(os.environ.get("PORT", "8080"))), Handler).serve_forever()
