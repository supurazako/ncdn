const player = document.querySelector("#moq-player");
const placeholder = document.querySelector("#video-placeholder");
const playerState = document.querySelector("#player-state");
const liveState = document.querySelector("#live-state");
const subscriberNode = document.querySelector("#subscriber-node");
const originNode = document.querySelector("#origin-node");
const subscriberDetail = document.querySelector("#subscriber-detail");
const events = document.querySelector("#events");
const lastFrame = document.querySelector("#last-frame");

const metricElements = {
  moq_relay_active_connections: document.querySelector("#metric-connections"),
  moq_relay_active_publishers: document.querySelector("#metric-publishers"),
  moq_relay_active_subscriptions: document.querySelector("#metric-subscriptions"),
  moq_relay_active_tracks: document.querySelector("#metric-tracks"),
};

function addEvent(kind, message) {
  const row = document.createElement("li");
  const now = new Date();
  row.innerHTML = `<time>${now.toLocaleTimeString()}</time><b>${kind}</b><span></span>`;
  row.querySelector("span").textContent = message;
  events.prepend(row);
  while (events.children.length > 50) events.lastElementChild.remove();
}

function setPlayerState(state, detail) {
  playerState.textContent = state;
  subscriberDetail.textContent = detail;
}

function parseMetrics(text) {
  const values = new Map();
  for (const line of text.split("\n")) {
    if (!line || line.startsWith("#")) continue;
    const [name, value] = line.trim().split(/\s+/, 2);
    values.set(name, value);
  }
  return values;
}

[
  ["loadeddata", "MEDIA", "first decodable frame received"],
  ["play", "PLAYER", "playback started"],
  ["pause", "PLAYER", "playback paused"],
  ["volumechange", "PLAYER", "volume changed"],
].forEach(([name, kind, message]) => {
  player.addEventListener(name, () => {
    addEvent(kind, message);
    if (name === "loadeddata" || name === "play") {
      placeholder.classList.add("hidden");
      subscriberNode.classList.add("online");
      liveState.className = "live-state live";
      liveState.innerHTML = "<span></span> LIVE";
      setPlayerState("playing", "receiving /demo");
    } else if (name === "pause") {
      setPlayerState("paused", "connected to /demo");
    }
  });
});

player.addEventListener("timeupdate", () => {
  lastFrame.textContent = `Frame received ${new Date().toLocaleTimeString()}`;
});

player.addEventListener("error", (event) => {
  subscriberNode.classList.add("error");
  liveState.className = "live-state error";
  liveState.innerHTML = "<span></span> ERROR";
  setPlayerState("error", "WebTransport connection failed");
  addEvent("ERROR", event.detail?.message || "player error");
});

async function refreshMetrics() {
  try {
    const edgeMetricsPath = window.selectedEdge === "c1" ? "/c1-metrics" : "/metrics";
    const [response, originResponse] = await Promise.all([
      fetch(edgeMetricsPath, { cache: "no-store" }),
      fetch("/origin-metrics", { cache: "no-store" }),
    ]);
    if (!response.ok) throw new Error(`edge metrics HTTP ${response.status}`);
    if (!originResponse.ok) throw new Error(`origin metrics HTTP ${originResponse.status}`);
    const values = parseMetrics(await response.text());
    const originValues = parseMetrics(await originResponse.text());
    for (const [name, element] of Object.entries(metricElements)) {
      element.textContent = values.get(name) ?? "0";
    }
    document.querySelector("#relay-node").classList.add("online");
    originNode.classList.add("online");
    if (Number(originValues.get("moq_relay_active_publishers") || 0) > 0) {
      document.querySelector("#publisher-node").classList.add("online");
    }
  } catch (error) {
    document.querySelector("#relay-node").classList.add("error");
    addEvent("METRICS", error.message);
  }
}

document.querySelector("#clear-events").addEventListener("click", () => events.replaceChildren());
addEvent("SYSTEM", "Visualizer initialized");
refreshMetrics();
setInterval(refreshMetrics, 2000);
