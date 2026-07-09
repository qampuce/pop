const http = require("http");

const PORT = process.env.PORT || 3000;

const providers = ["mock", "stripe", "mercadopago", "kushki", "dlocal", "niubiz"];

function sendJSON(res, data, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(data));
}

function sendError(res, message, status = 400) {
  sendJSON(res, { error: message }, status);
}

const server = http.createServer((req, res) => {
  if (req.url === "/health") {
    sendJSON(res, { 
      status: "ok", 
      service: "pop",
      version: "0.2.0",
      uptime: process.uptime()
    });
    return;
  }

  if (req.url === "/providers") {
    sendJSON(res, { providers });
    return;
  }

  if (req.url === "/") {
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end(`
      <html>
        <head><title>pop — Payment Orchestration Platform</title></head>
        <body>
          <h1>pop — Payment Orchestration Platform</h1>
          <p>SDK de orquestación de pasarelas de pago para SaaS multi-tenant y multi-país</p>
          <h2>Endpoints</h2>
          <ul>
            <li><a href="/health">GET /health</a> — Health check</li>
            <li><a href="/providers">GET /providers</a> — Lista de providers</li>
          </ul>
          <h2>Providers</h2>
          <ul>
            ${providers.map(p => `<li>${p}</li>`).join('')}
          </ul>
        </body>
      </html>
    `);
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/tokenize") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          provider_token: `tok_${Date.now()}`,
          vaulted: true,
          method: data.method || "card",
          last4: data.card?.last4 || "4242",
          brand: data.card?.brand || "visa"
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/charge") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          id: `pay_${Date.now()}`,
          status: "captured",
          amount: data.amount,
          provider: data.provider || "mock",
          reference: data.reference,
          created_at: new Date().toISOString()
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/authorize") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          id: `auth_${Date.now()}`,
          status: "authorized",
          amount: data.amount,
          provider: data.provider || "mock",
          reference: data.reference,
          created_at: new Date().toISOString()
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/capture") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          id: `pay_${Date.now()}`,
          status: "captured",
          amount: data.amount,
          provider: "mock",
          reference: data.authorization_id,
          created_at: new Date().toISOString()
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/refund") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          id: `ref_${Date.now()}`,
          status: "refunded",
          amount: data.amount,
          provider: "mock",
          reference: data.payment_id,
          created_at: new Date().toISOString()
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  if (req.method === "POST" && req.url === "/api/v1/void") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body);
        sendJSON(res, {
          id: `void_${Date.now()}`,
          status: "voided",
          provider: "mock",
          reference: data.authorization_id,
          created_at: new Date().toISOString()
        });
      } catch (e) {
        sendError(res, "Invalid JSON");
      }
    });
    return;
  }

  res.writeHead(404, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ error: "Not found" }));
});

server.listen(PORT, () => {
  console.log(`[pop] escuchando en puerto ${PORT}`);
});
