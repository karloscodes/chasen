const express = require("express");
const _ = require("lodash");

const app = express();

app.get("/up", function (_req, res) {
  res.status(200).type("text").send("ok\n");
});

app.get("/", function (req, res) {
  res.status(200).type("text").send("node " + process.version + " path " + _.escape(req.path) + "\n");
});

app.listen(3000, "0.0.0.0");
