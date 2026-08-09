const params = new URLSearchParams(location.search);
const requestedEdge = params.get("edge");
const requestedStrategy = params.get("strategy");

function loadScript(src) {
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = src;
    script.onload = resolve;
    script.onerror = () => reject(new Error(`failed to load ${src}`));
    document.body.appendChild(script);
  });
}

async function selectRoute() {
  if (requestedEdge === "c0" || requestedEdge === "c1") {
    const port = requestedEdge === "c1" ? 4444 : 4443;
    return {
      edge: requestedEdge,
      strategy: "manual",
      url: `https://100.94.113.55:${port}`,
    };
  }

  const strategy = requestedStrategy === "round-robin" ? "round-robin" : "rendezvous";
  const response = await fetch(`/route?namespace=${encodeURIComponent("/demo")}&strategy=${strategy}`, {
    cache: "no-store",
  });
  if (!response.ok) throw new Error(`routing API HTTP ${response.status}`);
  return response.json();
}

async function bootstrap() {
  const route = await selectRoute();
  const endpoint = new URL(route.url);

  window.selectedEdge = route.edge;
  window.selectedStrategy = route.strategy;
  document.querySelector("#moq-player").setAttribute("src", `${route.url}?namespace=demo`);
  document.querySelector("#transport-endpoint").textContent =
    `WebTransport → ${endpoint.hostname}:${endpoint.port}/udp`;
  document.querySelector("#edge-name").textContent = `Edge Relay ${route.edge.toUpperCase()}`;
  document.querySelector("#edge-detail").textContent =
    `${route.strategy} · UDP ${endpoint.port}`;

  const selector = route.strategy === "manual"
    ? `[data-edge="${route.edge}"]`
    : `[data-strategy="${route.strategy}"]`;
  document.querySelector(selector)?.classList.add("selected");

  await loadScript("https://cdn.jsdelivr.net/gh/video-dev/moq-js@6032b66cd7c6956612e7902a0caecd054154a572/demo/lib/moq-player.iife.js");
  await loadScript("app.js");
}

bootstrap().catch((error) => {
  document.querySelector("#live-state").className = "live-state error";
  document.querySelector("#live-state").innerHTML = "<span></span> ERROR";
  document.querySelector("#player-state").textContent = "routing error";
  document.querySelector("#transport-endpoint").textContent = error.message;
});
