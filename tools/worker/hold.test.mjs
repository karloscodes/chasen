// The Worker against a real server that is down at first and comes up later.
// Run: node --test tools/worker/hold.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import worker from "./hold.mjs";

function server(port) {
  const seen = [];
  const s = http.createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      seen.push(`${req.method} ${req.url} ${body}`);
      res.end("hello from the server");
    });
  });
  return new Promise((resolve) => s.listen(port, "127.0.0.1", () => resolve({ seen, close: () => s.close() })));
}

test("a request to a server that is down waits, and goes through once when the server is back", async () => {
  const started = Date.now();
  // The server comes up after four seconds, like after a short reboot.
  const up = new Promise((resolve) => setTimeout(() => resolve(server(4791)), 4000));

  const response = await worker.fetch(new Request("http://127.0.0.1:4791/cart", { method: "POST", body: "item=1" }));
  const origin = await up;

  assert.equal(response.status, 200);
  assert.equal(await response.text(), "hello from the server");
  assert.ok(Date.now() - started >= 4000, "it answered before the server was up");
  assert.deepEqual(origin.seen, ["POST /cart item=1"], "the server must get the request one time, with its body");
  origin.close();
});

test("a request to a server that is up goes through at once", async () => {
  const origin = await server(4792);
  const started = Date.now();

  const response = await worker.fetch(new Request("http://127.0.0.1:4792/"));

  assert.equal(response.status, 200);
  assert.ok(Date.now() - started < 1000);
  origin.close();
});

test("an error of the app itself is not tried again", async () => {
  const s = http.createServer((req, res) => { res.statusCode = 500; res.end("the app failed"); });
  await new Promise((resolve) => s.listen(4793, "127.0.0.1", resolve));
  const started = Date.now();

  const response = await worker.fetch(new Request("http://127.0.0.1:4793/"));

  assert.equal(response.status, 500);
  assert.ok(Date.now() - started < 1000, "it waited on an answer of the app");
  s.close();
});
