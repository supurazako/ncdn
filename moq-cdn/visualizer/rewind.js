export function rewindStartGroup(latestGroup, rewindSeconds, groupDurationSeconds) {
  if (!Number.isInteger(latestGroup) || latestGroup < 0) {
    throw new TypeError("latestGroup must be a non-negative integer");
  }
  if (!(rewindSeconds > 0) || !(groupDurationSeconds > 0)) {
    throw new TypeError("rewind and group duration must be positive");
  }
  return Math.max(0, latestGroup - Math.ceil(rewindSeconds / groupDurationSeconds));
}

export function playbackDelaySeconds(liveAnchorGroup, playbackGroup, groupDurationSeconds) {
  if (!Number.isInteger(liveAnchorGroup) || !Number.isInteger(playbackGroup)) return 0;
  return Math.max(0, liveAnchorGroup - playbackGroup) * groupDurationSeconds;
}

export function estimatedLiveGroup(liveAnchorGroup, elapsedSeconds, groupDurationSeconds) {
  if (!Number.isInteger(liveAnchorGroup) || liveAnchorGroup < 0) {
    throw new TypeError("liveAnchorGroup must be a non-negative integer");
  }
  if (!(elapsedSeconds >= 0) || !(groupDurationSeconds > 0)) {
    throw new TypeError("elapsed time must be non-negative and group duration must be positive");
  }
  return liveAnchorGroup + Math.floor(elapsedSeconds / groupDurationSeconds);
}
