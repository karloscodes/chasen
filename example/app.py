# The smallest app that follows the Chasen standard:
# it listens on $PORT, answers 200 on /up, keeps its SQLite database in
# $DATABASE_PATH, and stops on SIGTERM.
import http.server
import os
import signal
import sqlite3
import sys

# Rule 10: stop on SIGTERM. Python as the first process ignores it otherwise.
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))

db = sqlite3.connect(os.environ["DATABASE_PATH"])
db.execute("PRAGMA journal_mode=WAL")
db.execute("CREATE TABLE IF NOT EXISTS hits (at TEXT DEFAULT CURRENT_TIMESTAMP)")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = "ok"
        if self.path != "/up":
            db.execute("INSERT INTO hits DEFAULT VALUES")
            db.commit()
            hits = db.execute("SELECT count(*) FROM hits").fetchone()[0]
            body = f"hits={hits} version={os.environ['APP_VERSION']} greeting={os.environ.get('GREETING', '')}"
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body.encode())


http.server.HTTPServer(("", int(os.environ["PORT"])), Handler).serve_forever()
