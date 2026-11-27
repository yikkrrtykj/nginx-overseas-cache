"""Synthetic HTTPS origin used only by the disposable GitHub Actions runner."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ssl
import sys


class Origin(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    count = 0

    def do_GET(self):
        Origin.count += 1
        response = f"fixture-response-{Origin.count}".encode()
        if self.path.startswith("/render"):
            response += b'<link href="https://webcodercdn.atum.live/templates/test.woff2">'
        self.send_response(200)
        self.send_header("Content-Type", "text/html" if self.path.startswith("/render") else "application/octet-stream")
        self.send_header("Content-Length", str(len(response)))
        if "private" in self.path:
            self.send_header("Cache-Control", "private, no-store")
            self.send_header("Set-Cookie", "fixture=private; HttpOnly")
        self.end_headers()
        self.wfile.write(response)

    def log_message(self, *_args):
        pass


if __name__ == "__main__":
    server = ThreadingHTTPServer(("93.184.215.254", 443), Origin)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(sys.argv[1], sys.argv[2])
    server.socket = context.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
