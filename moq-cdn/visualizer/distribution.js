const colors = ["#4dd9ff", "#a88cff", "#ffbd5c", "#ff6b9b"];
const history = [];
let previous;

const state = document.querySelector("#connection-state");
const edgeGrid = document.querySelector("#edge-grid");
const splitBar = document.querySelector("#split-bar");
const splitLegend = document.querySelector("#split-legend");
const canvas = document.querySelector("#history");

function rate(value) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return `${Math.round(value)}`;
}

function bitRate(bytesPerSecond) {
  const bits = bytesPerSecond * 8;
  if (bits >= 1_000_000) return `${(bits / 1_000_000).toFixed(2)} Mbps`;
  if (bits >= 1_000) return `${(bits / 1_000).toFixed(1)} kbps`;
  return `${Math.round(bits)} bps`;
}

function deltas(snapshot) {
  const elapsed = previous ? Math.max(.1, (Date.parse(snapshot.timestamp) - Date.parse(previous.timestamp)) / 1000) : 1;
  return snapshot.backends.map((backend, index) => {
    const old = previous?.backends[index];
    return {
      ...backend,
      pps: old ? Math.max(0, backend.packets - old.packets) / elapsed : 0,
      bytesPerSecond: old ? Math.max(0, backend.bytes - old.bytes) / elapsed : 0
    };
  });
}

function drawHistory() {
  const context = canvas.getContext("2d");
  const width = canvas.clientWidth * devicePixelRatio;
  const height = canvas.clientHeight * devicePixelRatio;
  canvas.width = width; canvas.height = height;
  context.clearRect(0, 0, width, height);
  const max = Math.max(1, ...history.flatMap((point) => point));
  context.strokeStyle = "rgba(157,255,221,.10)";
  for (let row = 1; row < 4; row++) {
    const y = height * row / 4;
    context.beginPath(); context.moveTo(0, y); context.lineTo(width, y); context.stroke();
  }
  const count = Math.max(2, history.length);
  for (let backend = 0; backend < (history[0]?.length ?? 0); backend++) {
    context.strokeStyle = colors[backend % colors.length];
    context.lineWidth = 2 * devicePixelRatio;
    context.beginPath();
    history.forEach((point, index) => {
      const x = width * index / (count - 1);
      const y = height - point[backend] / max * (height - 8 * devicePixelRatio) - 4 * devicePixelRatio;
      if (index === 0) context.moveTo(x, y); else context.lineTo(x, y);
    });
    context.stroke();
  }
}

function render(snapshot) {
  const backends = deltas(snapshot);
  const totalPPS = backends.reduce((sum, backend) => sum + backend.pps, 0);
  const totalBytes = backends.reduce((sum, backend) => sum + backend.bytesPerSecond, 0);
  document.querySelector("#vip").textContent = snapshot.vip4;
  document.querySelector("#algorithm").textContent = snapshot.selection_algorithm;
  document.querySelector("#total-pps").textContent = `${rate(totalPPS)} pps`;
  document.querySelector("#total-rate").textContent = bitRate(totalBytes);
  document.querySelector("#lb-detail").textContent = `${snapshot.vip4}:4443`;
  document.querySelector("#updated-at").textContent = new Date(snapshot.timestamp).toLocaleTimeString();

  edgeGrid.replaceChildren(); splitBar.replaceChildren(); splitLegend.replaceChildren();
  backends.forEach((backend, index) => {
    const color = colors[index % colors.length];
    const share = totalPPS > 0 ? backend.pps / totalPPS * 100 : 100 / backends.length;
    const card = document.createElement("article");
    card.className = "edge node";
    card.style.setProperty("--edge", color);
    card.innerHTML = `<span>EDGE ${index}</span><strong><b></b><em></em></strong><small></small><i class="pulse"></i>`;
    card.querySelector("b").textContent = backend.id;
    card.querySelector("em").textContent = `${share.toFixed(1)}%`;
    card.querySelector("small").textContent = `${rate(backend.pps)} pps · ${bitRate(backend.bytesPerSecond)} · ${backend.address}`;
    card.style.setProperty("--play", backend.pps > 0 ? "running" : "paused");
    card.style.setProperty("--speed", `${Math.max(.35, 2.2 - Math.log10(backend.pps + 1))}s`);
    edgeGrid.append(card);

    const segment = document.createElement("i");
    segment.style.width = `${share}%`; segment.style.background = color;
    splitBar.append(segment);
    const legend = document.createElement("div");
    legend.className = "legend-row";
    legend.innerHTML = `<i></i><span></span><strong></strong>`;
    legend.querySelector("i").style.background = color;
    legend.querySelector("span").textContent = backend.id;
    legend.querySelector("strong").textContent = `${backend.packets.toLocaleString()} packets`;
    splitLegend.append(legend);
  });

  history.push(backends.map((backend) => backend.pps));
  if (history.length > 60) history.shift();
  drawHistory();
  previous = snapshot;
  state.className = "state live";
  state.innerHTML = "<i></i> LIVE";
}

async function refresh() {
  try {
    const response = await fetch("/api/distribution", { cache: "no-store" });
    if (!response.ok) throw new Error(`telemetry API returned ${response.status}`);
    render(await response.json());
  } catch (error) {
    state.className = "state error";
    state.innerHTML = "<i></i> OFFLINE";
    document.querySelector("#lb-detail").textContent = error.message;
  }
}

addEventListener("resize", drawHistory);
refresh();
setInterval(refresh, 1000);
