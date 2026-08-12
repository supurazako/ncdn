import assert from "node:assert/strict";
import test from "node:test";

import { estimatedLiveGroup, playbackDelaySeconds, rewindStartGroup } from "./rewind.js";

test("10秒を2秒Groupの5個前へ変換する", () => {
  assert.equal(rewindStartGroup(20, 10, 2), 15);
});

test("配信開始直後はGroup 0より前を要求しない", () => {
  assert.equal(rewindStartGroup(3, 10, 2), 0);
});

test("LIVE位置との差を秒へ変換する", () => {
  assert.equal(playbackDelaySeconds(20, 15, 2), 10);
});

test("経過時間に応じて推定LIVE Groupを進める", () => {
  assert.equal(estimatedLiveGroup(100, 9.9, 2), 104);
  assert.equal(estimatedLiveGroup(100, 10, 2), 105);
});
