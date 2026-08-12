import { estimatedLiveGroup, playbackDelaySeconds, rewindStartGroup } from "./rewind.js";

const playerMount = document.querySelector("#player-mount");
const placeholder = document.querySelector("#video-placeholder");
const playerState = document.querySelector("#player-state");
const liveState = document.querySelector("#live-state");
const events = document.querySelector("#events");
const lastFrame = document.querySelector("#last-frame");
const endpoint = document.querySelector("#transport-endpoint");
const subscriberDetail = document.querySelector("#subscriber-detail");
const channelSelect = document.querySelector("#channel-select");
const channelTitle = document.querySelector("#channel-title");
const edgeName = document.querySelector("#edge-name");
const broadcastName = document.querySelector("#broadcast-name");
const rewindButton = document.querySelector("#rewind-10");
const liveButton = document.querySelector("#return-live");
const playbackPosition = document.querySelector("#playback-position");

let player;
let renderObserver;
let connectGeneration = 0;
let selectedChannel;
let clientConfig;
let latestGroup;
let liveAnchorGroup;
let liveAnchorTime;
let requestedStartGroup;
let awaitingRewindStart = false;
let playbackMode = "live";

function addEvent(kind, message) {
  const row = document.createElement("li");
  const now = new Date();
  row.innerHTML = `<time>${now.toLocaleTimeString()}</time><b>${kind}</b><span></span>`;
  row.querySelector("span").textContent = message;
  events.prepend(row);
  while (events.children.length > 50) events.lastElementChild.remove();
}

function setConnectionState(state, detail) {
  playerState.textContent = state;
  liveState.className = `live-state ${state === "playing" ? "live" : state === "error" ? "error" : ""}`;
  liveState.innerHTML = `<span></span> ${state.toUpperCase()}`;
  subscriberDetail.textContent = detail;
}

function markLive(canvas) {
  placeholder.classList.add("hidden");
  setConnectionState("playing", "WebTransport + WebCodecs active");
  lastFrame.textContent = "Direct frame received";
  document.querySelector("#publisher-node").classList.add("online");
  document.querySelector("#origin-node").classList.add("online");
  document.querySelector("#subscriber-node").classList.add("online");
  addEvent("MEDIA", `WebCodecs rendered ${canvas.width}x${canvas.height}`);
}

function manualRelayURL() {
  const configured = new URLSearchParams(window.location.search).get("relay");
  if (!configured) return "";
  const host = configured.includes(":") && !configured.startsWith("[")
    ? `[${configured}]`
    : configured;
  return `http://${host}:4443/`;
}

async function publicMoQEndpoint() {
  const manual = manualRelayURL();
  const response = await fetch("/api/config");
  if (!response.ok) throw new Error(`config API returned ${response.status}`);
  const config = await response.json();
  return {
    ...config,
    url: manual || config.moq_url,
    source: manual ? "manual" : "shared VIP"
  };
}

function createPlayer(channel, route, generation, startGroup) {
  renderObserver?.disconnect();
  player?.remove();
  playerMount.replaceChildren();

  const ui = document.createElement("moq-watch-ui");
  player = document.createElement("moq-watch");
  player.id = "moq-player";
  player.setAttribute("name", channel.broadcast);
  player.setAttribute("muted", "");
  player.setAttribute("visible", "always");
  player.setAttribute("latency", "real-time");
  const canvas = document.createElement("canvas");
  player.append(canvas);
  ui.append(player);
  playerMount.append(ui);

  // This CDN experiment intentionally requires QUIC. A failed WebTransport
  // connection must not silently continue over WebSocket/TCP.
  player.connection.websocket = { enabled: false };
  if (route.certificate_sha256) {
    player.connection.webtransport = {
      serverCertificateHashes: [{
        algorithm: "sha-256",
        value: route.certificate_sha256
      }]
    };
  }
  if (startGroup === undefined) {
    delete globalThis.__NCDN_REWIND_START_GROUP;
  } else {
    globalThis.__NCDN_REWIND_START_GROUP = startGroup;
  }
  globalThis.__NCDN_PLAYER_GENERATION = generation;
  player.setAttribute("url", route.url);

  renderObserver = new MutationObserver(() => {
    if (generation === connectGeneration && canvas.width > 0 && canvas.height > 0) {
      markLive(canvas);
      renderObserver.disconnect();
    }
  });
  renderObserver.observe(canvas, { attributes: true, attributeFilter: ["width", "height"] });

  player.addEventListener("play", () => addEvent("PLAYER", `${channel.broadcast} playback started`));
  player.addEventListener("pause", () => addEvent("PLAYER", "playback paused"));
  player.addEventListener("error", (event) => {
    if (generation !== connectGeneration) return;
    const message = event.detail?.message || String(event.detail || "MoQ player error");
    addEvent("ERROR", message);
    setConnectionState("error", message);
  });
}

async function connectChannel(channel, startGroup) {
  const generation = ++connectGeneration;
  placeholder.classList.remove("hidden");
  setConnectionState("connecting", "Selecting an Edge");
  channelTitle.textContent = channel.id;
  broadcastName.textContent = channel.broadcast;
  lastFrame.textContent = "Waiting for frame";

  try {
    const route = await publicMoQEndpoint();
    if (generation !== connectGeneration) return;
    route.url = route.url.endsWith("/") ? route.url : `${route.url}/`;
    clientConfig = route;
    requestedStartGroup = startGroup;
    awaitingRewindStart = startGroup !== undefined;
    endpoint.textContent = route.url;
    edgeName.textContent = route.source;
    addEvent("ROUTE", `${channel.broadcast} → ${route.source}`);
    createPlayer(channel, route, generation, startGroup);
    addEvent("CONNECT", `${route.url} via WebTransport${startGroup === undefined ? "" : ` from Group ${startGroup}`}`);
  } catch (error) {
    addEvent("ERROR", error.message);
    setConnectionState("error", error.message);
  }
}

async function initialize() {
  const response = await fetch("/api/channels");
  if (!response.ok) throw new Error(`channel API returned ${response.status}`);
  const channels = await response.json();
  if (!channels.length) throw new Error("channel catalog is empty");

  for (const channel of channels) {
    const option = document.createElement("option");
    option.value = channel.id;
    option.textContent = `${channel.id} · ${channel.broadcast}`;
    channelSelect.append(option);
  }

  const requested = new URLSearchParams(window.location.search).get("channel");
  const initial = channels.find((channel) => channel.id === requested) || channels[0];
  selectedChannel = initial;
  channelSelect.value = initial.id;
  channelSelect.addEventListener("change", () => {
    const selected = channels.find((channel) => channel.id === channelSelect.value);
    selectedChannel = selected;
    playbackMode = "live";
    latestGroup = undefined;
    liveAnchorGroup = undefined;
    liveAnchorTime = undefined;
    rewindButton.disabled = true;
    liveButton.disabled = true;
    playbackPosition.textContent = "LIVE位置を取得中";
    const url = new URL(window.location.href);
    url.searchParams.set("channel", selected.id);
    history.replaceState(null, "", url);
    connectChannel(selected);
  });

  await connectChannel(initial);
}

globalThis.addEventListener("ncdn-moq-group", (event) => {
  if (!Number.isInteger(event.detail?.group) || event.detail.generation !== connectGeneration) return;
  const group = event.detail.group;
  if (playbackMode === "live") {
    latestGroup = Math.max(latestGroup ?? group, group);
    playbackPosition.textContent = `LIVE · Group ${group}`;
    rewindButton.disabled = false;
    liveButton.disabled = true;
    return;
  }

  const elapsedSeconds = liveAnchorTime === undefined ? 0 : (performance.now() - liveAnchorTime) / 1000;
  const estimatedLive = estimatedLiveGroup(
    liveAnchorGroup,
    elapsedSeconds,
    clientConfig.group_duration_seconds
  );
  const delay = playbackDelaySeconds(estimatedLive, group, clientConfig.group_duration_seconds);
  playbackPosition.textContent = `${delay}秒遅れ · Group ${group}`;
  if (awaitingRewindStart) {
    if (requestedStartGroup !== undefined && group > requestedStartGroup) {
      addEvent("CACHE", `Group ${requestedStartGroup}は失効済み、Group ${group}から再生`);
    }
    awaitingRewindStart = false;
  }
});

rewindButton.addEventListener("click", () => {
  if (!selectedChannel || latestGroup === undefined || !clientConfig) return;
  liveAnchorGroup = latestGroup;
  liveAnchorTime = performance.now();
  const target = rewindStartGroup(latestGroup, 10, clientConfig.group_duration_seconds);
  playbackMode = "rewind";
  rewindButton.disabled = true;
  liveButton.disabled = false;
  playbackPosition.textContent = `Group ${target}を要求中`;
  addEvent("REWIND", `Group ${latestGroup} → ${target}`);
  connectChannel(selectedChannel, target);
});

liveButton.addEventListener("click", () => {
  if (!selectedChannel) return;
  playbackMode = "live";
  latestGroup = undefined;
  liveAnchorGroup = undefined;
  liveAnchorTime = undefined;
  requestedStartGroup = undefined;
  awaitingRewindStart = false;
  rewindButton.disabled = true;
  liveButton.disabled = true;
  playbackPosition.textContent = "LIVE位置へ復帰中";
  addEvent("LIVE", "latest Groupへ再購読");
  connectChannel(selectedChannel);
});

document.querySelector("#clear-events").addEventListener("click", () => events.replaceChildren());
addEvent("SYSTEM", "MoQ multi-channel player initialized");
initialize().catch((error) => {
  addEvent("ERROR", error.message);
  setConnectionState("error", error.message);
});
