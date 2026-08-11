const http = require("node:http");

http.createServer((_, response) => response.end("node\n"))
  .listen(Number(process.env.PORT || 8080));
