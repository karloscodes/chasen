# The smallest app that follows the Chasen standard:
# it listens on $PORT, answers 200 on /up, keeps its SQLite database in
# $DATABASE_PATH, and stops on SIGTERM.
import http.server
import os
import signal
import sqlite3
import sys

# Rule 10: stop on SIGTERM. Python as the first process ignores it otherwise.
# A deploy stops the old version this way: a worker in the app finishes its
# job here. This one writes down that it stopped, next to its database.
def stop(*_):
    with open(os.path.join(os.path.dirname(os.environ["DATABASE_PATH"]), "stops.log"), "a") as log:
        log.write(f"stopped {os.environ['APP_VERSION']}\n")
    sys.exit(0)


signal.signal(signal.SIGTERM, stop)

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
