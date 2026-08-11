const player = document.querySelector("#moq-player");
const canvas = player.querySelector("canvas");
const placeholder = document.querySelector("#video-placeholder");
const playerState = document.querySelector("#player-state");
const liveState = document.querySelector("#live-state");
const events = document.querySelector("#events");
const lastFrame = document.querySelector("#last-frame");
const endpoint = document.querySelector("#transport-endpoint");
const subscriberDetail = document.querySelector("#subscriber-detail");

function relayHost() {
  const configured = new URLSearchParams(window.location.search).get("relay");
  const host = configured || window.location.hostname || "localhost";
  return host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
}

const relayUrl = `http://${relayHost()}:4443/`;

function addEvent(kind, message) {
  const row = document.createElement("li");
  const now = new Date();
  row.innerHTML = `<time>${now.toLocaleTimeString()}</time><b>${kind}</b><span></span>`;
  row.querySelector("span").textContent = message;
  events.prepend(row);
  while (events.children.length > 50) events.lastElementChild.remove();
}

function markLive() {
  placeholder.classList.add("hidden");
  playerState.textContent = "playing";
  liveState.className = "live-state live";
  liveState.innerHTML = "<span></span> LIVE";
  subscriberDetail.textContent = "WebTransport + WebCodecs active";
  lastFrame.textContent = "Direct frame received";
  document.querySelector("#origin-node").classList.add("online");
  document.querySelector("#subscriber-node").classList.add("online");
}

endpoint.textContent = relayUrl;
playerState.textContent = "connecting";
// This experiment evaluates direct MoQ over QUIC. Do not silently turn a
// failed WebTransport connection into a TCP/WebSocket session.
player.connection.websocket = { enabled: false };
player.setAttribute("url", relayUrl);
addEvent("CONNECT", `${relayUrl} via WebTransport`);

const rendered = new MutationObserver(() => {
  if (canvas.width > 0 && canvas.height > 0) {
    markLive();
    addEvent("MEDIA", `WebCodecs rendered ${canvas.width}x${canvas.height}`);
    rendered.disconnect();
  }
});
rendered.observe(canvas, { attributes: true, attributeFilter: ["width", "height"] });

player.addEventListener("play", () => addEvent("PLAYER", "direct MoQ playback started"));
player.addEventListener("pause", () => addEvent("PLAYER", "playback paused"));
player.addEventListener("error", (event) => {
  const message = event.detail?.message || String(event.detail || "MoQ player error");
  addEvent("ERROR", message);
  playerState.textContent = "error";
  liveState.className = "live-state error";
  liveState.innerHTML = "<span></span> ERROR";
});

document.querySelector("#clear-events").addEventListener("click", () => events.replaceChildren());
addEvent("SYSTEM", "moq-dev direct player initialized");
