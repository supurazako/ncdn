function loadScript(src) {
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = src;
    script.onload = resolve;
    script.onerror = () => reject(new Error(`failed to load ${src}`));
    document.head.appendChild(script);
  });
}

loadScript("https://cdn.jsdelivr.net/npm/hls.js@1.6.13/dist/hls.min.js")
  .then(() => loadScript("app.js"))
  .catch((error) => {
    document.querySelector("#player-state").textContent = "player load error";
    document.querySelector("#transport-endpoint").textContent = error.message;
  });
