try {
  await import("./vendor/watch-element.js");
  await import("./vendor/watch-ui.js");
  await import("./app.js");
} catch (error) {
  document.querySelector("#player-state").textContent = "player load error";
  document.querySelector("#transport-endpoint").textContent = error.message;
}
