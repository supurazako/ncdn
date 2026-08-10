const video = document.querySelector("#moq-player");
const placeholder = document.querySelector("#video-placeholder");
const playerState = document.querySelector("#player-state");
const liveState = document.querySelector("#live-state");
const events = document.querySelector("#events");
const lastFrame = document.querySelector("#last-frame");
const liveButton = document.querySelector("#go-live");
const manifest = "/hls/demo.hang/master.m3u8";

function addEvent(kind, message) {
  const row = document.createElement("li");
  const now = new Date();
  row.innerHTML = `<time>${now.toLocaleTimeString()}</time><b>${kind}</b><span></span>`;
  row.querySelector("span").textContent = message;
  events.prepend(row);
  while (events.children.length > 50) events.lastElementChild.remove();
}

function updateLiveState() {
  if (!Number.isFinite(video.duration) || video.duration === 0) return;
  const behind = Math.max(0, video.duration - video.currentTime);
  const live = behind < 3;
  liveState.className = `live-state ${live ? "live" : ""}`;
  liveState.innerHTML = `<span></span> ${live ? "LIVE" : `${behind.toFixed(1)}s BEHIND`}`;
  lastFrame.textContent = `DVR window ${video.duration.toFixed(1)}s`;
}

if (Hls.isSupported()) {
  const hls = new Hls({
    lowLatencyMode: true,
    liveSyncDurationCount: 1,
    liveMaxLatencyDurationCount: 3,
  });
  hls.loadSource(manifest);
  hls.attachMedia(video);
  hls.on(Hls.Events.MANIFEST_PARSED, () => {
    playerState.textContent = "ready";
    addEvent("MANIFEST", "MoQ FETCH-backed HLS timeline loaded");
    video.play().catch(() => {});
  });
  hls.on(Hls.Events.ERROR, (_event, data) => {
    addEvent("ERROR", `${data.type}: ${data.details}`);
    if (data.fatal) {
      playerState.textContent = "error";
      liveState.className = "live-state error";
      liveState.innerHTML = "<span></span> ERROR";
    }
  });
} else if (video.canPlayType("application/vnd.apple.mpegurl")) {
  video.src = manifest;
} else {
  playerState.textContent = "HLS unsupported";
}

video.addEventListener("loadeddata", () => {
  placeholder.classList.add("hidden");
  playerState.textContent = "playing";
  addEvent("MEDIA", "first decodable frame received");
});
video.addEventListener("timeupdate", updateLiveState);
video.addEventListener("pause", () => addEvent("PLAYER", "paused inside DVR window"));
video.addEventListener("play", () => addEvent("PLAYER", "playback resumed"));

liveButton.addEventListener("click", () => {
  if (Number.isFinite(video.duration)) video.currentTime = video.duration;
  video.play().catch(() => {});
  addEvent("PLAYER", "jumped to live edge");
});

document.querySelector("#clear-events").addEventListener("click", () => events.replaceChildren());
addEvent("SYSTEM", "moq-dev/moq DVR player initialized");
