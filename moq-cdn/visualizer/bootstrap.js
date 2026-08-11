try {
  await import("https://esm.sh/@moq/watch@0.4.5/element");
  await import("https://esm.sh/@moq/watch@0.4.5/ui");
  await import("./app.js");
} catch (error) {
  document.querySelector("#player-state").textContent = "player load error";
  document.querySelector("#transport-endpoint").textContent = error.message;
}
